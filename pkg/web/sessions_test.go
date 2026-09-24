package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// issueSession выполняет реальный bootstrap сессии через HTTP и возвращает
// cookie и CSRF-токен — тот же путь, которым пользуется браузер.
func issueSession(t *testing.T, srv *Server) (*http.Cookie, string) {
	t.Helper()
	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, newLoopbackRequest(http.MethodGet, "/api/session", nil))
	if writer.Code != http.StatusOK {
		t.Fatalf("session bootstrap: %d %s", writer.Code, writer.Body.String())
	}
	var response sessionResponse
	if err := json.NewDecoder(writer.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	cookies := writer.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("session cookie отсутствует: %v", cookies)
	}
	return cookies[0], response.CSRFToken
}

func sessionCount(srv *Server) int {
	srv.sessionMu.Lock()
	defer srv.sessionMu.Unlock()
	return len(srv.sessions)
}

// TestSessionStoreEvictsExpiredEntries фиксирует исходный дефект QS-19:
// истёкшие записи не удалялись никогда, поэтому новая сессия только
// увеличивала карту (5000 → 5001) вместо того, чтобы вытеснить мёртвые.
func TestSessionStoreEvictsExpiredEntries(t *testing.T) {
	srv, _ := newTestServer(t)
	for i := 0; i < 32; i++ {
		issueSession(t, srv)
	}
	if got := sessionCount(srv); got != 32 {
		t.Fatalf("после 32 выдач ожидалось 32 сессии, получено %d", got)
	}

	srv.sessionMu.Lock()
	for token, session := range srv.sessions {
		session.ExpiresAt = time.Now().UTC().Add(-time.Hour)
		srv.sessions[token] = session
	}
	srv.sessionMu.Unlock()

	issueSession(t, srv)
	if got := sessionCount(srv); got != 1 {
		t.Fatalf("истёкшие не вытеснены: после новой сессии в карте %d записей, ожидалась 1", got)
	}
}

// TestSessionStoreEnforcesUpperBound проверяет верхнюю границу на живых
// (неистёкших) сессиях: карта не растёт дальше maxBrowserSessions, а самая
// свежая сессия остаётся рабочей.
func TestSessionStoreEnforcesUpperBound(t *testing.T) {
	controller := &fakeRunController{}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	for i := 0; i < maxBrowserSessions+200; i++ {
		cookie, csrf := issueSession(t, srv)
		if got := sessionCount(srv); got > maxBrowserSessions {
			t.Fatalf("после %d выдач в карте %d сессий, граница %d", i+1, got, maxBrowserSessions)
		}
		// Последняя выданная сессия обязана работать: политика при переполнении —
		// вытеснить самую старую, а не отказать новому клиенту.
		if i == maxBrowserSessions+199 {
			request := newLoopbackRequest(http.MethodPost, "/api/runs", strings.NewReader(`{"feature":"f","task":"t"}`))
			request.AddCookie(cookie)
			request.Header.Set("X-CSRF-Token", csrf)
			request.Header.Set("Content-Type", "application/json")
			writer := httptest.NewRecorder()
			srv.router.ServeHTTP(writer, request)
			if writer.Code != http.StatusAccepted {
				t.Fatalf("новейшая сессия отклонена: %d %s", writer.Code, writer.Body.String())
			}
		}
	}
	if got := sessionCount(srv); got != maxBrowserSessions {
		t.Fatalf("итоговый размер карты %d, ожидалось %d", got, maxBrowserSessions)
	}
}

// TestSessionStoreEvictsOldestFirst проверяет выбранную политику: при
// достижении границы выбывает самая старая сессия, остальные живут.
func TestSessionStoreEvictsOldestFirst(t *testing.T) {
	srv, _ := newTestServer(t)
	oldestCookie, _ := issueSession(t, srv)
	secondCookie, _ := issueSession(t, srv)
	for i := 0; i < maxBrowserSessions-1; i++ {
		issueSession(t, srv)
	}

	request := newLoopbackRequest(http.MethodGet, "/api/pipelines", nil)
	request.AddCookie(oldestCookie)
	if _, ok := srv.requestSession(request); ok {
		t.Fatal("самая старая сессия должна быть вытеснена при достижении границы")
	}
	request = newLoopbackRequest(http.MethodGet, "/api/pipelines", nil)
	request.AddCookie(secondCookie)
	if _, ok := srv.requestSession(request); !ok {
		t.Fatal("вторая по старшинству сессия вытеснена преждевременно")
	}
}

// TestSessionStoreKeepsLegitimateSessionAlive — легитимный сценарий: сессия,
// выданная браузеру, продолжает работать после того, как рядом выдали и
// бросили множество других сессий (вкладки, перезагрузки), пока граница не
// достигнута.
func TestSessionStoreKeepsLegitimateSessionAlive(t *testing.T) {
	controller := &fakeRunController{}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	cookie, csrf := issueSession(t, srv)
	for i := 0; i < 100; i++ {
		issueSession(t, srv)
	}

	request := newLoopbackRequest(http.MethodPost, "/api/runs", strings.NewReader(`{"feature":"feat","task":"задача"}`))
	request.AddCookie(cookie)
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Content-Type", "application/json")
	writer := httptest.NewRecorder()
	srv.router.ServeHTTP(writer, request)
	if writer.Code != http.StatusAccepted {
		t.Fatalf("легитимная сессия сломана: %d %s", writer.Code, writer.Body.String())
	}
	if controller.startCalls != 1 {
		t.Fatalf("команда не дошла до контроллера: calls=%d", controller.startCalls)
	}
}
