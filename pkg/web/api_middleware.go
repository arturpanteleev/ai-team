package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

const maxIdempotencyBody = maxSubmissionBody

type apiResponseError struct {
	Error          string   `json:"error"`
	Code           string   `json:"code"`
	AllowedActions []string `json:"allowed_actions,omitempty"`
}

type apiJSONErrorMiddleware struct{ next http.Handler }

func (m apiJSONErrorMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	captured := newAPICapturedResponse()
	m.next.ServeHTTP(captured, r)
	status := captured.status
	if status == 0 {
		status = http.StatusOK
	}
	body := captured.body.Bytes()
	header := captured.header
	if status >= 400 && !isJSONError(body) {
		message := strings.TrimSpace(string(body))
		body, _ = json.Marshal(apiResponseError{Error: message, Code: apiErrorCode(status)})
		body = append(body, '\n')
		header = header.Clone()
		header.Set("Content-Type", "application/json; charset=utf-8")
		header.Del("Content-Length")
	}
	copyHeaders(w.Header(), header)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (s *Server) apiJSONErrors(next http.Handler) http.Handler {
	return apiJSONErrorMiddleware{next: next}
}

func apiErrorCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusConflict:
		return "conflict"
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case http.StatusServiceUnavailable:
		return "unavailable"
	default:
		if status >= 500 {
			return "internal_error"
		}
		return "request_failed"
	}
}

func isJSONError(body []byte) bool {
	var value struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	return json.Unmarshal(bytes.TrimSpace(body), &value) == nil && value.Error != "" && value.Code != ""
}

type apiCapturedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newAPICapturedResponse() *apiCapturedResponse {
	return &apiCapturedResponse{header: make(http.Header)}
}

func (w *apiCapturedResponse) Header() http.Header { return w.header }

func (w *apiCapturedResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *apiCapturedResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(body)
}

func copyHeaders(destination, source http.Header) {
	for key, values := range source {
		destination[key] = append([]string(nil), values...)
	}
}

type apiIdempotencyMiddleware struct {
	store *store.Store
	next  http.Handler
}

func (m apiIdempotencyMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if requestKey == "" {
		requestKey = strings.TrimSpace(r.Header.Get("X-Request-Key"))
	}
	if requestKey == "" {
		m.next.ServeHTTP(w, r)
		return
	}
	if len(requestKey) > 128 || strings.ContainsAny(requestKey, "\r\n\x00") {
		writeJSONAPIError(w, http.StatusBadRequest, "idempotency key must be 1-128 visible characters", "invalid_request_key")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxIdempotencyBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(strings.ToLower(err.Error()), "request body too large") {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSONAPIError(w, status, "request body could not be read", apiErrorCode(status))
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	session, _ := sessionFromRequest(r)
	scope := session.Principal.ActorID
	if scope == "" {
		scope = "local-user"
	}
	requestHash := sha256.Sum256([]byte(r.Method + "\x00" + r.URL.RequestURI() + "\x00" + string(body)))
	fingerprint := hex.EncodeToString(requestHash[:])
	stored, claimed, err := m.store.BeginIdempotency(scope, requestKey, fingerprint)
	if err != nil {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "idempotency store unavailable", "idempotency_unavailable")
		return
	}
	if !claimed {
		switch {
		case stored.Conflict:
			writeJSONAPIError(w, http.StatusConflict, "idempotency key was already used for different request bytes", "idempotency_conflict")
		case stored.InProgress:
			writeJSONAPIError(w, http.StatusConflict, "request with this idempotency key is still in progress", "idempotency_in_progress")
		default:
			copyHeaders(w.Header(), http.Header(stored.Headers))
			w.WriteHeader(stored.StatusCode)
			_, _ = w.Write(stored.Body)
		}
		return
	}
	captured := newAPICapturedResponse()
	m.next.ServeHTTP(captured, r)
	status := captured.status
	if status == 0 {
		status = http.StatusOK
	}
	if err := m.store.CompleteIdempotency(scope, requestKey, fingerprint, status, map[string][]string(captured.header), captured.body.Bytes()); err != nil {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "response could not be recorded for retry", "idempotency_unavailable")
		return
	}
	copyHeaders(w.Header(), captured.header)
	w.WriteHeader(status)
	_, _ = w.Write(captured.body.Bytes())
}

func sessionFromRequest(r *http.Request) (browserSession, bool) {
	if session, ok := r.Context().Value(requestSessionContextKey{}).(browserSession); ok {
		return session, true
	}
	return browserSession{}, false
}

// writeSecurity's session lookup is copied into context only after the
// authenticated middleware has validated the cookie, so key scope is never
// supplied by the request body.
type requestSessionContextKey struct{}

func writeJSONAPIError(w http.ResponseWriter, status int, message, code string) {
	writeJSONResponse(w, status, apiResponseError{Error: message, Code: code})
}

func (s *Server) taskReadAPI(next http.Handler) http.Handler {
	return s.apiJSONErrors(s.readSecurity(next))
}

func (s *Server) taskWriteAPI(next http.Handler) http.Handler {
	return s.apiJSONErrors(s.writeSecurity(apiIdempotencyMiddleware{store: s.store, next: next}))
}
