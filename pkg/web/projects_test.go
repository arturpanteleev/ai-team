package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arturpanteleev/ai-team/pkg/autopickup"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func projectServe(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	srv.router.ServeHTTP(response, authorizedRequest(t, srv, method, path, body))
	return response
}

func decodeProject(t *testing.T, response *httptest.ResponseRecorder) store.Project {
	t.Helper()
	var project store.Project
	if err := json.Unmarshal(response.Body.Bytes(), &project); err != nil {
		t.Fatalf("decode project: %v %s", err, response.Body.String())
	}
	return project
}

func newProjectTestServer(t *testing.T) (*Server, *taskCreateCapturingController) {
	t.Helper()
	controller := &taskCreateCapturingController{fakeRunController: &fakeRunController{}}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithRunController(controller))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, controller
}

func TestProjectsAPICreateListUpdateArchive(t *testing.T) {
	srv, _ := newProjectTestServer(t)

	listed := projectServe(t, srv, http.MethodGet, "/api/projects", "")
	var list struct {
		Projects []store.Project `json:"projects"`
	}
	if listed.Code != http.StatusOK || json.Unmarshal(listed.Body.Bytes(), &list) != nil ||
		len(list.Projects) != 1 || list.Projects[0].ID != store.DefaultProjectID || list.Projects[0].Name != store.DefaultProjectName {
		t.Fatalf("clean install must list the default project: %d %s", listed.Code, listed.Body.String())
	}

	created := projectServe(t, srv, http.MethodPost, "/api/projects", `{"name":"  Платформа ","description":"Задачи платформы"}`)
	project := decodeProject(t, created)
	if created.Code != http.StatusCreated || project.Name != "Платформа" || project.Archived || project.ID <= store.DefaultProjectID {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	for _, body := range []string{`{"name":""}`, `{"name":"   "}`, `{}`, `{"name":"x","unknown":1}`,
		`{"name":"` + strings.Repeat("я", store.MaxProjectNameBytes) + `"}`} {
		if response := projectServe(t, srv, http.MethodPost, "/api/projects", body); response.Code != http.StatusBadRequest {
			t.Fatalf("invalid create %.40s: %d %s", body, response.Code, response.Body.String())
		}
	}
	if duplicate := projectServe(t, srv, http.MethodPost, "/api/projects", `{"name":"платформа"}`); duplicate.Code != http.StatusConflict ||
		!strings.Contains(duplicate.Body.String(), "project_name_taken") {
		t.Fatalf("name must be unique ignoring case: %d %s", duplicate.Code, duplicate.Body.String())
	}

	path := fmt.Sprintf("/api/projects/%d", project.ID)
	updated := decodeProject(t, projectServe(t, srv, http.MethodPatch, path, `{"description":"Новое описание"}`))
	if updated.Name != "Платформа" || updated.Description != "Новое описание" {
		t.Fatalf("partial update: %+v", updated)
	}
	if response := projectServe(t, srv, http.MethodPatch, path, `{}`); response.Code != http.StatusBadRequest {
		t.Fatalf("empty update: %d", response.Code)
	}
	if response := projectServe(t, srv, http.MethodGet, "/api/projects/999", ""); response.Code != http.StatusNotFound {
		t.Fatalf("unknown project: %d", response.Code)
	}
	if response := projectServe(t, srv, http.MethodDelete, path, ""); response.Code == http.StatusOK || response.Code == http.StatusNoContent {
		t.Fatalf("projects must not be physically deletable: %d", response.Code)
	}

	archived := decodeProject(t, projectServe(t, srv, http.MethodPost, path+"/archive", ""))
	if !archived.Archived || archived.ArchivedAt == nil {
		t.Fatalf("archive: %+v", archived)
	}
	again := decodeProject(t, projectServe(t, srv, http.MethodPost, path+"/archive", ""))
	if !again.ArchivedAt.Equal(*archived.ArchivedAt) {
		t.Fatalf("repeat archive must be idempotent: %+v vs %+v", again, archived)
	}
	list.Projects = nil
	if json.Unmarshal(projectServe(t, srv, http.MethodGet, "/api/projects", "").Body.Bytes(), &list) != nil || len(list.Projects) != 1 {
		t.Fatalf("archived project must be hidden by default: %+v", list.Projects)
	}
	list.Projects = nil
	if json.Unmarshal(projectServe(t, srv, http.MethodGet, "/api/projects?include_archived=true", "").Body.Bytes(), &list) != nil || len(list.Projects) != 2 {
		t.Fatalf("archived project must be readable on request: %+v", list.Projects)
	}
	if response := projectServe(t, srv, http.MethodGet, path, ""); response.Code != http.StatusOK || !decodeProject(t, response).Archived {
		t.Fatalf("archived project must stay readable: %d %s", response.Code, response.Body.String())
	}
	restored := decodeProject(t, projectServe(t, srv, http.MethodPost, path+"/unarchive", ""))
	if restored.Archived || restored.ArchivedAt != nil {
		t.Fatalf("unarchive: %+v", restored)
	}
}

func TestTaskCreationRequiresActiveProject(t *testing.T) {
	srv, controller := newProjectTestServer(t)
	archived := decodeProject(t, projectServe(t, srv, http.MethodPost, "/api/projects", `{"name":"Старый"}`))
	projectServe(t, srv, http.MethodPost, fmt.Sprintf("/api/projects/%d/archive", archived.ID), "")

	cases := []struct {
		name, body string
		status     int
		code       string
	}{
		{"missing project", `{"feature":"f","task":"t"}`, http.StatusBadRequest, "project_required"},
		{"zero project", `{"project_id":0,"feature":"f","task":"t"}`, http.StatusBadRequest, "project_required"},
		{"unknown project", `{"project_id":999,"feature":"f","task":"t"}`, http.StatusNotFound, "project_not_found"},
		{"archived project", fmt.Sprintf(`{"project_id":%d,"feature":"f","task":"t"}`, archived.ID), http.StatusConflict, "project_archived"},
		{"missing project advanced", `{"feature":"f","task":"t","title":"T","mode":"manual"}`, http.StatusBadRequest, "project_required"},
	}
	for _, tc := range cases {
		response := projectServe(t, srv, http.MethodPost, "/api/runs", tc.body)
		if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.code) {
			t.Fatalf("%s: %d %s", tc.name, response.Code, response.Body.String())
		}
	}
	if controller.startCalls != 0 {
		t.Fatalf("a rejected task must never reach the controller: %d starts", controller.startCalls)
	}
	runs, err := srv.store.GetPipelineRuns()
	if err != nil || len(runs) != 0 {
		t.Fatalf("rejected tasks left runs behind: %+v %v", runs, err)
	}

	ok := projectServe(t, srv, http.MethodPost, "/api/runs", `{"project_id":1,"feature":"f","task":"t"}`)
	if ok.Code != http.StatusAccepted {
		t.Fatalf("valid task: %d %s", ok.Code, ok.Body.String())
	}
	run, err := srv.store.GetPipelineRunByRunID("run-created")
	if err != nil || run.ProjectID != store.DefaultProjectID {
		t.Fatalf("admitted run must carry its project: %+v %v", run, err)
	}
	if !strings.Contains(run.ConfigSnapshot, `"project_id":1`) {
		t.Fatalf("admission snapshot must keep the project for recovery: %s", run.ConfigSnapshot)
	}
}

func TestRunAndBacklogListsFilterByProject(t *testing.T) {
	srv, _ := newProjectTestServer(t)
	other := decodeProject(t, projectServe(t, srv, http.MethodPost, "/api/projects", `{"name":"Другой"}`))
	empty := decodeProject(t, projectServe(t, srv, http.MethodPost, "/api/projects", `{"name":"Пустой"}`))
	now := time.Now().UTC()
	for runID, projectID := range map[string]int64{"run-main": store.DefaultProjectID, "run-other": other.ID} {
		if err := srv.store.AdmitPipelineRun(&store.PipelineRun{RunID: runID, ProjectID: projectID, Feature: "f", Status: "queued", StartedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	runIDs := func(path string) []string {
		var body struct {
			Runs []taskRunListItem `json:"runs"`
		}
		response := projectServe(t, srv, http.MethodGet, path, "")
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
		ids := []string{}
		for _, run := range body.Runs {
			ids = append(ids, run.RunID)
		}
		return ids
	}
	if got := runIDs(fmt.Sprintf("/api/runs?project_id=%d", other.ID)); len(got) != 1 || got[0] != "run-other" {
		t.Fatalf("filter by project: %v", got)
	}
	if got := runIDs(fmt.Sprintf("/api/runs?project_id=%d", empty.ID)); len(got) != 0 {
		t.Fatalf("new project must be empty: %v", got)
	}
	if got := runIDs("/api/runs"); len(got) != 2 {
		t.Fatalf("no filter lists everything: %v", got)
	}
	if response := projectServe(t, srv, http.MethodGet, "/api/runs?project_id=abc", ""); response.Code != http.StatusBadRequest {
		t.Fatalf("bad filter must be rejected: %d", response.Code)
	}

	for _, body := range []string{
		fmt.Sprintf(`{"project_id":%d,"title":"Идея","feature":"idea-other"}`, other.ID),
		`{"project_id":1,"title":"Идея 2","feature":"idea-main"}`,
	} {
		if response := projectServe(t, srv, http.MethodPost, "/api/backlog", body); response.Code != http.StatusCreated {
			t.Fatalf("backlog create: %d %s", response.Code, response.Body.String())
		}
	}
	backlog := func(path string) int {
		var body struct {
			Items []store.BacklogItem `json:"items"`
		}
		response := projectServe(t, srv, http.MethodGet, path, "")
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &body) != nil {
			t.Fatalf("%s: %d %s", path, response.Code, response.Body.String())
		}
		return len(body.Items)
	}
	if backlog(fmt.Sprintf("/api/backlog?project_id=%d", other.ID)) != 1 || backlog(fmt.Sprintf("/api/backlog?project_id=%d", empty.ID)) != 0 || backlog("/api/backlog") != 2 {
		t.Fatal("backlog project filter is wrong")
	}
}

func TestBacklogRequiresActiveProject(t *testing.T) {
	srv, _ := newProjectTestServer(t)
	archived := decodeProject(t, projectServe(t, srv, http.MethodPost, "/api/projects", `{"name":"Архив"}`))
	projectServe(t, srv, http.MethodPost, fmt.Sprintf("/api/projects/%d/archive", archived.ID), "")
	for body, status := range map[string]int{
		`{"title":"x","feature":"x"}`:                                           http.StatusBadRequest,
		`{"project_id":777,"title":"x","feature":"x"}`:                          http.StatusNotFound,
		fmt.Sprintf(`{"project_id":%d,"title":"x","feature":"x"}`, archived.ID): http.StatusConflict,
	} {
		if response := projectServe(t, srv, http.MethodPost, "/api/backlog", body); response.Code != status {
			t.Fatalf("%s: %d %s", body, response.Code, response.Body.String())
		}
	}
}

func TestAutoPickupKeepsProjectAndFailsForArchivedProject(t *testing.T) {
	srv, _ := newPickupServer(t, ":memory:", t.TempDir(), pickupPolicy(2, 100), &fixedLedger{}, pickupTestNow)
	project, err := srv.store.CreateProject("Автоподбор", "", "po-1", pickupTestNow)
	if err != nil {
		t.Fatal(err)
	}
	item, err := srv.store.CreateBacklogItem(project.ID, "Идея", "Текст", "pickup-project", "po-1", pickupTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.SetBacklogItemStatus(item.ID, autopickup.StatusReady, "po-1", pickupTestNow); err != nil {
		t.Fatal(err)
	}
	if decision, err := srv.runAutoPickup(context.Background()); err != nil || !decision.Pick {
		t.Fatalf("pickup: %+v %v", decision, err)
	}
	run, err := srv.store.GetPipelineRunByRunID("run-created")
	if err != nil || run.ProjectID != project.ID {
		t.Fatalf("auto pickup must keep the project: %+v %v", run, err)
	}

	// Проект заархивирован после пометки ready: задача не создаётся, элемент
	// получает видимую причину отказа.
	second, err := srv.store.CreateBacklogItem(project.ID, "Вторая", "Текст", "pickup-archived", "po-1", pickupTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.SetBacklogItemStatus(second.ID, autopickup.StatusReady, "po-1", pickupTestNow); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.store.SetProjectArchived(project.ID, true, pickupTestNow); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.runAutoPickup(context.Background()); err != nil {
		t.Fatal(err)
	}
	failed, err := srv.store.GetBacklogItem(second.ID)
	if err != nil || failed.Status != autopickup.StatusFailed || !strings.Contains(failed.Error, "archived") {
		t.Fatalf("archived project must fail the pickup visibly: %+v %v", failed, err)
	}
}

func TestProjectManagementUsesRBAC(t *testing.T) {
	manager, err := cloudidentity.NewTokenManager([]byte(strings.Repeat("p", 32)))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(":memory:", "", t.TempDir(), WithAuthenticator(manager), WithRunController(&fakeRunController{}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	ownerCookie, ownerCSRF := cloudSessionForTest(t, srv, manager, "owner@example.com", cloudidentity.RoleProductOwner)
	// Права роли определяет членство в команде, а не заявленная роль в токене.
	memberSession := func(role cloudidentity.Role) (*http.Cookie, string) {
		email := string(role) + "@example.com"
		invite := teamRequest(srv, ownerCookie, ownerCSRF, http.MethodPost, "/api/team/invitations", fmt.Sprintf(`{"email":%q,"roles":[%q]}`, email, role))
		var invitation struct {
			Token string `json:"activation_token"`
		}
		if invite.Code != http.StatusCreated || json.Unmarshal(invite.Body.Bytes(), &invitation) != nil {
			t.Fatalf("invite %s: %d %s", role, invite.Code, invite.Body.String())
		}
		activate := newLoopbackRequest(http.MethodPost, "/api/team/activate", strings.NewReader(`{"token":"`+invitation.Token+`"}`))
		activate.Header.Set("Content-Type", "application/json")
		activated := httptest.NewRecorder()
		srv.router.ServeHTTP(activated, activate)
		if activated.Code != http.StatusOK {
			t.Fatalf("activate %s: %d %s", role, activated.Code, activated.Body.String())
		}
		return cloudSessionForTest(t, srv, manager, email, cloudidentity.RoleProductOwner)
	}
	for _, tc := range []struct {
		role      cloudidentity.Role
		canManage bool
	}{
		{cloudidentity.RoleProductOwner, true}, {cloudidentity.RoleArchitect, true},
		{cloudidentity.RoleDeveloper, false}, {cloudidentity.RoleQA, false},
		{cloudidentity.RoleReviewer, false}, {cloudidentity.RoleReleaseManager, false},
	} {
		cookie, csrf := ownerCookie, ownerCSRF
		actor := "owner@example.com"
		if tc.role != cloudidentity.RoleProductOwner {
			cookie, csrf = memberSession(tc.role)
			actor = string(tc.role) + "@example.com"
		}
		create := teamRequest(srv, cookie, csrf, http.MethodPost, "/api/projects", fmt.Sprintf(`{"name":"Проект %s"}`, tc.role))
		if tc.canManage {
			var project store.Project
			if create.Code != http.StatusCreated || json.Unmarshal(create.Body.Bytes(), &project) != nil || project.CreatedBy != actor {
				t.Fatalf("%s create: %d %s", tc.role, create.Code, create.Body.String())
			}
		} else if create.Code != http.StatusForbidden {
			t.Fatalf("%s must not create projects: %d %s", tc.role, create.Code, create.Body.String())
		}
		archive := teamRequest(srv, cookie, csrf, http.MethodPost, "/api/projects/1/archive", "")
		if tc.canManage != (archive.Code == http.StatusOK) || (!tc.canManage && archive.Code != http.StatusForbidden) {
			t.Fatalf("%s archive: %d %s", tc.role, archive.Code, archive.Body.String())
		}
		if tc.canManage {
			if restore := teamRequest(srv, cookie, csrf, http.MethodPost, "/api/projects/1/unarchive", ""); restore.Code != http.StatusOK {
				t.Fatalf("%s unarchive: %d", tc.role, restore.Code)
			}
		}
		if read := teamRequest(srv, cookie, csrf, http.MethodGet, "/api/projects", ""); read.Code != http.StatusOK {
			t.Fatalf("%s must read projects: %d", tc.role, read.Code)
		}
	}
}

// После рестарта восстановление admission берёт проект из долговечной строки
// run: он не теряется вместе с памятью процесса.
func TestRunProjectSurvivesStoreReopen(t *testing.T) {
	path := t.TempDir() + "/web.db"
	first, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	project, err := first.CreateProject("Долговечный", "", "po", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.AdmitPipelineRun(&store.PipelineRun{RunID: "durable", ProjectID: project.ID, Feature: "f", Status: "queued", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	second, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	pending, err := second.PendingAdmissions()
	if err != nil || len(pending) != 1 || pending[0].ProjectID != project.ID {
		t.Fatalf("pending admission lost its project: %+v %v", pending, err)
	}
}
