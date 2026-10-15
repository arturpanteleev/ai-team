package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/arturpanteleev/ai-team/pkg/approval"
	"github.com/arturpanteleev/ai-team/pkg/cloudidentity"
	"github.com/arturpanteleev/ai-team/pkg/config"
	"github.com/arturpanteleev/ai-team/pkg/evidence"
	"github.com/arturpanteleev/ai-team/pkg/humandecision"
	"github.com/arturpanteleev/ai-team/pkg/strictjson"
	"github.com/arturpanteleev/ai-team/pkg/web/store"
)

func randomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

type sessionResponse struct {
	CSRFToken string                   `json:"csrf_token"`
	Principal *cloudidentity.Principal `json:"principal,omitempty"`
}

func (s *Server) resolveTeamPrincipal(principal cloudidentity.Principal) (cloudidentity.Principal, int64, error) {
	return s.resolveTeamPrincipalOpts(principal, true)
}

// resolveTeamPrincipalOpts is resolveTeamPrincipal with an explicit switch for
// the first-admin bootstrap. Only an interactive sign-in may create the first
// team member; the MCP endpoint never does.
func (s *Server) resolveTeamPrincipalOpts(principal cloudidentity.Principal, bootstrap bool) (cloudidentity.Principal, int64, error) {
	if s.authenticator == nil || s.localAuth {
		return principal, 0, nil
	}
	member, err := s.store.TeamMember(principal.ActorID)
	if err != nil {
		return cloudidentity.Principal{}, 0, err
	}
	if member == nil {
		count, countErr := s.store.TeamMemberCount()
		if countErr != nil {
			return cloudidentity.Principal{}, 0, countErr
		}
		if bootstrap && count == 0 && principal.Has(cloudidentity.RoleProductOwner) {
			if err = s.store.BootstrapTeamAdmin(principal.ActorID, time.Now().UTC()); err != nil {
				// Another first login may have won the bootstrap race.
				member, err = s.store.TeamMember(principal.ActorID)
				if err != nil || member == nil {
					return cloudidentity.Principal{}, 0, errors.New("team registry is already initialized")
				}
			} else {
				var lookupErr error
				member, lookupErr = s.store.TeamMember(principal.ActorID)
				if lookupErr != nil {
					return cloudidentity.Principal{}, 0, lookupErr
				}
			}
		}
	}
	if member == nil || member.Status != "active" {
		return cloudidentity.Principal{}, 0, errors.New("пользователь не активен в команде")
	}
	roles, err := cloudidentity.ParseRoles(member.Roles)
	if err != nil {
		return cloudidentity.Principal{}, 0, err
	}
	canonical, err := cloudidentity.NewPrincipal(member.ActorID, roles)
	return canonical, member.SessionEpoch, err
}

func (s *Server) handleAuthConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSONResponse(w, http.StatusOK, map[string]bool{
		"authentication_required": s.authenticator != nil,
		"team_management_enabled": s.authenticator != nil && !s.localAuth,
		"sso_enabled":             s.oidc != nil,
	})
}

func (s *Server) handleCurrentIdentity(w http.ResponseWriter, r *http.Request) {
	response := map[string]any{"authentication_required": s.authenticator != nil}
	if session, ok := s.requestSession(r); ok && s.authenticator != nil {
		response["principal"] = session.Principal
	}
	writeJSONResponse(w, http.StatusOK, response)
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if s.authenticator == nil {
		http.Error(w, "требуется Bearer token", http.StatusUnauthorized)
		return
	}
	// An authenticated browser recovers its CSRF token from the HttpOnly
	// session cookie after reload. This endpoint is deliberately same-origin:
	// don't disclose the token to a request that cannot prove it came from this
	// origin, even though the cookie is also SameSite=Strict.
	if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" &&
		strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		if !isSameOriginSessionRequest(r) {
			http.Error(w, "требуется same-origin session request", http.StatusForbidden)
			return
		}
		if session, ok := s.requestSession(r); ok {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Add("Vary", "Cookie")
			response := sessionResponse{CSRFToken: session.CSRFToken}
			if s.authenticator != nil {
				response.Principal = &session.Principal
			}
			writeJSONResponse(w, http.StatusOK, response)
			return
		}
		if s.authenticator != nil {
			http.Error(w, "сессия истекла, войдите снова", http.StatusUnauthorized)
			return
		}
	}

	var principal cloudidentity.Principal
	var sessionEpoch int64
	if s.authenticator != nil {
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(header, "Bearer ") {
			http.Error(w, "требуется Bearer token", http.StatusUnauthorized)
			return
		}
		var err error
		principal, err = s.authenticator.Verify(strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")))
		if err != nil {
			http.Error(w, "authentication failed", http.StatusUnauthorized)
			return
		}
		principal, sessionEpoch, err = s.resolveTeamPrincipal(principal)
		if err != nil {
			http.Error(w, "участник команды не активен или доступ отозван", http.StatusForbidden)
			return
		}
	}
	method := sessionMethodToken
	if s.localAuth {
		method = sessionMethodLocal
	}
	sessionToken, csrfToken, err := s.createBrowserSession(principal, sessionEpoch, method, "")
	if err != nil {
		http.Error(w, "session unavailable", http.StatusInternalServerError)
		return
	}
	s.setSessionCookie(w, r, sessionToken)
	response := sessionResponse{CSRFToken: csrfToken}
	if s.authenticator != nil {
		response.Principal = &principal
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Cookie")
	writeJSONResponse(w, http.StatusOK, response)
}

func isSameOriginSessionRequest(r *http.Request) bool {
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		return originMatchesRequest(r, origin)
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "same-origin")
}

func (s *Server) writeSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authenticator == nil {
			http.Error(w, "требуется локальный или cloud bearer token", http.StatusUnauthorized)
			return
		}
		session, ok := s.requestSession(r)
		if !ok {
			http.Error(w, "требуется web session", http.StatusUnauthorized)
			return
		}
		if !constantTimeEqual(r.Header.Get("X-CSRF-Token"), session.CSRFToken) {
			http.Error(w, "неверный CSRF token", http.StatusForbidden)
			return
		}
		if s.controller == nil {
			http.Error(w, "web control plane не настроен", http.StatusServiceUnavailable)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), requestSessionContextKey{}, session))
		next.ServeHTTP(w, r)
	})
}

func (s *Server) readSecurity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authenticator != nil {
			if _, ok := s.requestSession(r); !ok {
				http.Error(w, "требуется web session", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requestSession(r *http.Request) (browserSession, bool) {
	// An in-process MCP call carries the principal its bearer token resolved
	// to. The context key is unexported and set only by mcpDispatch, so no
	// network request can supply it.
	if session, ok := r.Context().Value(mcpSessionContextKey{}).(browserSession); ok {
		return session, true
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return browserSession{}, false
	}
	return s.lookupBrowserSession(cookie.Value)
}

func (s *Server) authorize(r *http.Request, permission cloudidentity.Permission, role cloudidentity.Role) error {
	if s.authenticator == nil {
		return errors.New("требуется аутентификация")
	}
	session, ok := s.requestSession(r)
	if !ok {
		return errors.New("требуется web session")
	}
	return cloudidentity.Authorize(session.Principal, permission, role)
}

func (s *Server) localApprovalRole(runID, approvalID, action, actorID string) (string, cloudidentity.Role, error) {
	if s.controller == nil {
		return string(cloudidentity.RoleProductOwner), cloudidentity.RoleProductOwner, nil
	}
	values, err := s.controller.Approvals(runID)
	if err != nil {
		return "", "", err
	}
	for _, pending := range values {
		if pending.ID != approvalID {
			continue
		}
		var payload struct {
			Kind string `json:"kind"`
		}
		_ = json.Unmarshal(pending.Payload, &payload)
		if action == "approve_spec" || payload.Kind == "agreed_spec" {
			return string(cloudidentity.RoleProductOwner), cloudidentity.RoleProductOwner, nil
		}
		// The decision service refuses a vote in a role somebody already
		// covered, so the local owner (who holds every role) votes in the first
		// role nobody voted for.
		votedRoles := make(map[string]bool)
		for _, decision := range pending.Decisions {
			votedRoles[decision.ActorRole] = true
		}
		for _, roleName := range pending.RequiredRoles {
			authorizationRole := canonicalStageRole(roleName)
			if !knownCloudRole(authorizationRole) {
				continue
			}
			if votedRoles[roleName] {
				continue
			}
			return roleName, authorizationRole, nil
		}
		break
	}
	// If no pending approval was found, the controller will reject the command;
	// use the local default only so authorization remains server-derived.
	return string(cloudidentity.RoleProductOwner), cloudidentity.RoleProductOwner, nil
}

func knownCloudRole(role cloudidentity.Role) bool {
	switch role {
	case cloudidentity.RoleProductOwner, cloudidentity.RoleArchitect, cloudidentity.RoleDeveloper,
		cloudidentity.RoleReviewer, cloudidentity.RoleQA, cloudidentity.RoleReleaseManager:
		return true
	default:
		return false
	}
}

func constantTimeEqual(actual, expected string) bool {
	if len(actual) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

type startRunCommand struct {
	ProjectID       int64    `json:"project_id"`
	Feature         string   `json:"feature"`
	Task            string   `json:"task"`
	Title           string   `json:"title,omitempty"`
	Idea            string   `json:"idea,omitempty"`
	Template        string   `json:"template,omitempty"`
	TemplateVersion string   `json:"template_version,omitempty"`
	Mode            string   `json:"mode,omitempty"`
	ExternalLinks   []string `json:"external_links,omitempty"`
}

func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r, cloudidentity.PermissionStart, ""); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	var command startRunCommand
	if err := decodeCommand(w, r, &command); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if command.Mode != "" && command.Mode != "manual" && command.Mode != "auto" {
		writeJSONAPIError(w, http.StatusBadRequest, "mode must be manual or auto", "bad_mode")
		return
	}
	actorID := "local-user"
	if session, ok := s.requestSession(r); ok && session.Principal.ActorID != "" {
		actorID = session.Principal.ActorID
	}
	runID, version, admissionFailed, err := s.startTask(r.Context(), command, actorID, nil)
	if err != nil && writeProjectError(w, err) {
		return
	}
	if errors.Is(err, errTaskCreateUnavailable) {
		writeJSONAPIError(w, http.StatusServiceUnavailable, "task create options are not connected to the task controller", "controller_action_unavailable")
		return
	}
	if err != nil {
		status := http.StatusBadRequest
		if admissionFailed {
			status = http.StatusServiceUnavailable
		}
		http.Error(w, err.Error(), status)
		return
	}
	response := map[string]string{"run_id": runID}
	if version != "" {
		response["template_version"] = version
	}
	writeJSONResponse(w, http.StatusAccepted, response)
}

// errTaskCreateUnavailable — контроллер не поддерживает расширенное создание
// задачи (режим, идея, ссылки); такой запрос не превращается молча в legacy.
var errTaskCreateUnavailable = errors.New("task create options are not connected to the task controller")

// startTask — единственный путь создания задачи для HTTP и автоподбора:
// закрепление шаблона, видимая admission-запись и событие run_queued.
// beforeAdmit выполняется первым внутри admission, до появления run в
// проекции; его ошибка отменяет запуск.
func (s *Server) startTask(ctx context.Context, command startRunCommand, actorID string, beforeAdmit func(runID string) error) (string, string, bool, error) {
	// Задача принадлежит ровно одному активному проекту; ранний отказ не
	// запускает контроллер. Окончательная проверка повторяется в транзакции admission.
	if err := s.store.RequireActiveProject(command.ProjectID); err != nil {
		return "", "", false, err
	}
	now := time.Now().UTC()
	admissionFailed := false
	admit := func(runID string) error {
		if beforeAdmit != nil {
			if err := beforeAdmit(runID); err != nil {
				admissionFailed = true
				return err
			}
		}
		s.templateMu.Lock()
		defer s.templateMu.Unlock()
		if templateStore, storeErr := s.templateStore(); storeErr != nil {
			return storeErr
		} else if version, pinErr := templateStore.PinCurrentForRun(runID, runID); pinErr == nil {
			if command.TemplateVersion != "" && command.TemplateVersion != version {
				return fmt.Errorf("requested template version is not the active project template")
			}
			if command.Template != "" {
				data, _, found, readErr := templateStore.ReadPinnedRun(runID)
				if readErr != nil || !found {
					if readErr == nil {
						readErr = os.ErrNotExist
					}
					return readErr
				}
				cfg, parseErr := config.ParseYAML(data)
				if parseErr != nil || cfg.Template != command.Template {
					return fmt.Errorf("requested template does not match the active project template")
				}
			}
			command.TemplateVersion = version
		} else if !errors.Is(pinErr, os.ErrNotExist) {
			return fmt.Errorf("pin project template: %w", pinErr)
		}
		admissionSnapshot, marshalErr := json.Marshal(command)
		if marshalErr != nil {
			return fmt.Errorf("invalid admission snapshot: %w", marshalErr)
		}
		if err := s.store.AdmitPipelineRun(&store.PipelineRun{
			RunID: runID, Feature: command.Feature, ProjectID: command.ProjectID, Status: "queued", StartedAt: now, ConfigSnapshot: string(admissionSnapshot),
		}); err != nil {
			admissionFailed = true
			return err
		}
		return nil
	}
	advancedRequest := command.Mode != "" || command.Title != "" || command.Idea != "" || len(command.ExternalLinks) > 0
	var runID string
	var err error
	if advancedRequest {
		controller, ok := s.controller.(TaskCreateController)
		if !ok {
			return "", "", false, errTaskCreateUnavailable
		}
		runID, err = controller.StartTask(ctx, command.Feature, command.Task, command.Title,
			command.Idea, command.Mode, command.ExternalLinks, admit)
	} else {
		runID, err = s.controller.StartWithAdmission(command.Feature, command.Task, admit)
	}
	if err != nil {
		return "", "", admissionFailed, err
	}
	s.appendRunEvent(runID, "run_queued", now, map[string]any{"status": "queued", "actor_id": actorID, "template_version": command.TemplateVersion})
	return runID, command.TemplateVersion, false, nil
}

// resumeCommand — тело POST /api/runs/{runID}/resume. Пустое тело — resume
// без выдачи бюджета. Новый бюджет задачи выдаётся только явным
// grant_budget_allowance с причиной.
type resumeCommand struct {
	GrantBudgetAllowance bool   `json:"grant_budget_allowance"`
	Reason               string `json:"reason"`
	// UpgradeRuntime accepts that the controller binary changed since the run
	// was started. It is an explicit, journaled decision, never the default.
	UpgradeRuntime bool `json:"upgrade_runtime"`
}

// budgetAllowanceResumer — контроллер, умеющий передать в pipeline явную
// выдачу бюджета задачи при resume.
type budgetAllowanceResumer interface {
	ResumeWithBudgetAllowance(runID, actorID, reason string) error
}

// runtimeUpgradeResumer — контроллер, умеющий продолжить run на новом
// бинарнике контроллера с явной фиксацией смены runtime в evidence.
type runtimeUpgradeResumer interface {
	ResumeWithRuntimeUpgrade(runID string) error
}

func decodeResumeCommand(w http.ResponseWriter, r *http.Request) (resumeCommand, error) {
	var command resumeCommand
	r.Body = http.MaxBytesReader(w, r.Body, maxCommandBody)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return command, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return command, nil
	}
	if err := strictjson.Unmarshal(data, maxCommandBody, &command); err != nil {
		return command, err
	}
	command.Reason = strings.TrimSpace(command.Reason)
	if command.GrantBudgetAllowance && command.Reason == "" {
		return command, errors.New("grant_budget_allowance требует непустую reason")
	}
	if !command.GrantBudgetAllowance && command.Reason != "" {
		return command, errors.New("reason задаётся только вместе с grant_budget_allowance")
	}
	if command.UpgradeRuntime && command.GrantBudgetAllowance {
		return command, errors.New("upgrade_runtime нельзя сочетать с grant_budget_allowance")
	}
	return command, nil
}

func (s *Server) handleResumeRun(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r, cloudidentity.PermissionResume, ""); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	command, err := decodeResumeCommand(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	runID := chi.URLParam(r, "runID")
	session, _ := s.requestSession(r)
	var resumer budgetAllowanceResumer
	if command.GrantBudgetAllowance {
		// Выдача относится только к ещё не покрытой выдачей остановке по
		// бюджету задачи. Дневной бюджет не выдаётся никогда.
		if !s.pendingBudgetStop(runID) {
			http.Error(w, "нет остановки по бюджету задачи, для которой можно выдать бюджет", http.StatusConflict)
			return
		}
		var ok bool
		if resumer, ok = s.controller.(budgetAllowanceResumer); !ok {
			http.Error(w, "контроллер не поддерживает выдачу бюджета", http.StatusNotImplemented)
			return
		}
	}
	// Resuming an agent stage launches its agent: record the actor first so
	// forbid_self_approval sees the launch; an unrecorded launch must not run.
	if err := s.recordResumeAgentLaunch(runID, session.Principal.ActorID); errors.Is(err, errResumeAwaitingDecision) || errors.Is(err, errResumeRunActive) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	} else if err != nil {
		http.Error(w, "agent launch could not be recorded", http.StatusServiceUnavailable)
		return
	}
	switch {
	case resumer != nil:
		err = resumer.ResumeWithBudgetAllowance(runID, session.Principal.ActorID, command.Reason)
	case command.UpgradeRuntime:
		upgrader, ok := s.controller.(runtimeUpgradeResumer)
		if !ok {
			http.Error(w, "контроллер не поддерживает смену runtime при resume", http.StatusNotImplemented)
			return
		}
		err = upgrader.ResumeWithRuntimeUpgrade(runID)
	default:
		err = s.controller.Resume(runID)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	// Кто снял паузу и выдал ли он бюджет — часть аудита. Resume без явной
	// выдачи новый бюджет задачи не даёт: задача снова встанет на паузу,
	// если бюджет исчерпан.
	data := map[string]any{
		"actor_id": session.Principal.ActorID, "budget_allowance_granted": command.GrantBudgetAllowance,
	}
	if command.UpgradeRuntime {
		data["runtime_upgrade"] = true
	}
	if command.GrantBudgetAllowance {
		data["reason"] = command.Reason
	}
	s.appendRunEvent(runID, "run_resume_requested", time.Now().UTC(), data)
	writeJSONResponse(w, http.StatusAccepted, map[string]string{"run_id": runID})
}

// pendingBudgetStop сообщает, есть ли у задачи остановка по бюджету задачи,
// ещё не покрытая явной выдачей бюджета: последнее budget_exceeded имеет
// scope=task и новее последней выдачи (run_resume_requested с
// budget_allowance_granted=true или budget_allowance_granted из evidence).
func (s *Server) pendingBudgetStop(runID string) bool {
	stop, err := s.store.LatestRunEvent(runID, "budget_exceeded")
	if err != nil || stop == nil {
		return false
	}
	var data struct {
		Scope string `json:"scope"`
	}
	if json.Unmarshal([]byte(stop.DataJSON), &data) != nil || data.Scope != "task" {
		return false
	}
	granted, err := s.store.LatestRunEvent(runID, "budget_allowance_granted")
	if err != nil || (granted != nil && granted.Sequence > stop.Sequence) {
		return false
	}
	resumed, err := s.store.LatestRunEvent(runID, "run_resume_requested")
	if err != nil {
		return false
	}
	if resumed == nil || resumed.Sequence < stop.Sequence {
		return true
	}
	var request struct {
		Granted bool `json:"budget_allowance_granted"`
	}
	return json.Unmarshal([]byte(resumed.DataJSON), &request) == nil && !request.Granted
}

func (s *Server) handleRetryDelivery(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r, cloudidentity.PermissionDeliver, ""); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err := requireEmptyCommand(w, r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	runID := chi.URLParam(r, "runID")
	run, err := s.store.GetPipelineRunByRunID(runID)
	if err != nil || run == nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	actorID := "local-user"
	if session, ok := s.requestSession(r); ok && session.Principal.ActorID != "" {
		actorID = session.Principal.ActorID
	}
	requestedAt := time.Now().UTC()
	// Persist intent before the controller can run Git/hosting commands. Later
	// outcome events are projections; a missing one cannot erase this audit row.
	if err := s.appendRunEventRequired(runID, "delivery_retry_requested", requestedAt, map[string]any{"actor_id": actorID}); err != nil {
		fmt.Fprintf(os.Stderr, "⚠ delivery retry intent audit for run %s: %v\n", runID, err)
		http.Error(w, "delivery retry could not be recorded", http.StatusServiceUnavailable)
		return
	}
	record, err := s.controller.DeliverDeferred(r.Context(), runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "⚠ delivery retry failed for run %s: %v\n", runID, err)
		s.appendRunEvent(runID, "delivery_retry_failed", time.Now().UTC(), map[string]any{
			"actor_id": actorID, "error": "delivery retry failed; see controller diagnostics",
		})
		http.Error(w, "delivery retry failed", http.StatusConflict)
		return
	}
	s.appendRunEvent(runID, "delivery_retry_succeeded", time.Now().UTC(), map[string]any{
		"actor_id": actorID, "plan_hash": record.PlanHash, "commit_sha": record.CommitSHA, "pr_url": record.PRURL,
	})
	writeJSONResponse(w, http.StatusOK, map[string]any{"run_id": runID, "delivery": record})
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r, cloudidentity.PermissionCancel, ""); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if err := requireEmptyCommand(w, r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	runID := chi.URLParam(r, "runID")
	actorID := "local-user"
	if session, ok := s.requestSession(r); ok && session.Principal.ActorID != "" {
		actorID = session.Principal.ActorID
	}
	if err := s.controller.Cancel(runID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.appendRunEvent(runID, "run_cancel_requested", time.Now().UTC(), map[string]any{"actor_id": actorID})
	if canceled, err := s.store.MarkQueuedCanceled(runID, time.Now().UTC()); err == nil && canceled {
		s.appendRunEvent(runID, "run_canceled", time.Now().UTC(), map[string]any{"status": "canceled"})
	}
	writeJSONResponse(w, http.StatusAccepted, map[string]string{"run_id": runID})
}

type decisionCommand struct {
	ActorID           string            `json:"actor_id"`
	ActorRole         string            `json:"actor_role"`
	Action            string            `json:"action"`
	Comment           string            `json:"comment,omitempty"`
	SubjectHash       string            `json:"subject_hash"`
	ArtifactRevisions map[string]string `json:"artifact_revisions,omitempty"`
}

func (s *Server) handleDecision(w http.ResponseWriter, r *http.Request) {
	var command decisionCommand
	if err := decodeCommand(w, r, &command); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Grill actions carry stage-specific semantics. In particular, new_version
	// must compare the selected result bytes with the pinned baseline before the
	// approval is resolved; the generic decision endpoint accepts client-supplied
	// artifact revisions and therefore cannot safely resolve grill approvals.
	var values []approval.PendingApproval
	if s.controller != nil {
		var err error
		values, err = s.controller.Approvals(chi.URLParam(r, "runID"))
		if err != nil {
			http.Error(w, "approval state unavailable", http.StatusInternalServerError)
			return
		}
		for _, pending := range values {
			if pending.ID == chi.URLParam(r, "approvalID") &&
				(pending.Kind == approval.KindGrillQuestions || pending.Trigger == approval.GrillTrigger) {
				http.Error(w, "grill approvals must use the stage-scoped grill action endpoint", http.StatusConflict)
				return
			}
			// The override of a failed required gate is a system administrator
			// action with its own endpoint (and a mandatory reason, an immutable
			// record bound to the result version). The generic endpoint must not
			// be a way around the right; a retry names gates and has its own too.
			if pending.ID == chi.URLParam(r, "approvalID") && pending.Kind == approval.KindCheckDecision &&
				(command.Action == "override" || command.Action == approval.ActionRetryGate) {
				if command.Action == "override" {
					if err := s.authorize(r, cloudidentity.PermissionGateOverride, ""); err != nil {
						http.Error(w, "only a system administrator may override a failed gate: "+err.Error(), http.StatusForbidden)
						return
					}
				}
				http.Error(w, "check decisions must use the stage-scoped override and gate retry endpoints", http.StatusConflict)
				return
			}
		}
	}
	actorID := command.ActorID
	actorRole := strings.TrimSpace(command.ActorRole)
	authorizationRole := canonicalStageRole(actorRole)
	if s.authenticator != nil {
		if s.localAuth {
			session, ok := s.requestSession(r)
			if !ok {
				http.Error(w, "требуется web session", http.StatusUnauthorized)
				return
			}
			actorID = session.Principal.ActorID
			var err error
			actorRole, authorizationRole, err = s.localApprovalRole(chi.URLParam(r, "runID"), chi.URLParam(r, "approvalID"), command.Action, actorID)
			if err != nil {
				http.Error(w, "не удалось определить назначенную роль approval", http.StatusInternalServerError)
				return
			}
		}
		if err := s.authorize(r, cloudidentity.PermissionDecision, authorizationRole); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		session, _ := s.requestSession(r)
		actorID = session.Principal.ActorID
		// The approval store accepts only its required roles. A member who holds
		// the claimed role but is not an assigned approver (e.g. the stage
		// executor when approver_function is set) is refused here with 403,
		// before the store is touched, instead of a store conflict.
		if pending, found := approvalByID(values, chi.URLParam(r, "approvalID")); found && !pending.Deferred &&
			!containsApprovalRole(pending.RequiredRoles, actorRole) {
			http.Error(w, "cloud RBAC: роль "+actorRole+" не назначена этому подтверждению", http.StatusForbidden)
			return
		}
	}
	if command.Action == "answer_questions" || command.Action == "approve_spec" || command.Action == "approve" || command.Action == "reject" {
		approvals, listErr := s.controller.Approvals(chi.URLParam(r, "runID"))
		if listErr != nil {
			http.Error(w, "не удалось проверить human approval", http.StatusInternalServerError)
			return
		}
		var matched *approval.PendingApproval
		for _, pending := range approvals {
			if pending.ID != chi.URLParam(r, "approvalID") {
				continue
			}
			matched = &pending
			break
		}
		if matched != nil {
			var payload struct {
				Kind string `json:"kind"`
			}
			_ = json.Unmarshal(matched.Payload, &payload)
			if command.Action == "answer_questions" {
				if matched.Kind != approval.KindQuestions || payload.Kind != "questions" {
					http.Error(w, "answer_questions разрешён только для запроса kind questions", http.StatusConflict)
					return
				}
				if strings.TrimSpace(command.Comment) == "" || len(command.Comment) > 16<<10 {
					http.Error(w, "ответ должен содержать от 1 до 16384 байт", http.StatusBadRequest)
					return
				}
			}
			if payload.Kind == "agreed_spec" {
				if !containsApprovalRole(matched.RequiredRoles, "product_owner") {
					http.Error(w, "согласование ТЗ не назначено Product Owner", http.StatusConflict)
					return
				}
				if command.Action != "approve_spec" {
					http.Error(w, "согласование ТЗ требует действия approve_spec", http.StatusConflict)
					return
				}
				if authorizationRole != cloudidentity.RoleProductOwner {
					http.Error(w, "согласовать ТЗ может только Product Owner", http.StatusForbidden)
					return
				}
			} else if command.Action == "approve_spec" {
				http.Error(w, "approve_spec разрешён только для Product Owner согласования ТЗ", http.StatusConflict)
				return
			}
			// forbid_self_approval is enforced by the decision service below.
		} else if command.Action == "answer_questions" || command.Action == "approve_spec" {
			http.Error(w, "не найден соответствующий Product Owner approval", http.StatusConflict)
			return
		}
	}
	// A decision on a stage transition goes through the one decision service
	// (roles, exact hash, self-approval, audit) shared with REST and MCP.
	if pending, found := approvalByID(values, chi.URLParam(r, "approvalID")); found && humandecision.IsTransition(pending) {
		channel, requestKey := decisionOrigin(r, humandecision.ChannelREST)
		decided, ok := s.decideTransition(w, humandecision.Request{
			RunID: chi.URLParam(r, "runID"), ApprovalID: pending.ID, Action: command.Action,
			SubjectHash: command.SubjectHash, Comment: command.Comment, ArtifactRevisions: command.ArtifactRevisions,
			Channel: channel, RequestKey: requestKey,
			Actor: humandecision.Actor{ID: actorID, Authenticated: s.authenticator != nil,
				HasRole: func(required string) bool { return required == actorRole }},
		})
		if !ok {
			return
		}
		writeJSONResponse(w, http.StatusOK, decided.Approval)
		return
	}
	selectedRevisions := command.ArtifactRevisions
	pinnedSelection := false
	if len(selectedRevisions) == 0 {
		// A quorum-all approval pins its artifacts on the first vote. Reuse that
		// exact selection for later voters even if somebody has appended a newer
		// human revision while the approval remains pending. The first decision
		// is authoritative even when it pinned an empty selection (for example,
		// when the artifact had no human revision yet).
		values, listErr := s.controller.Approvals(chi.URLParam(r, "runID"))
		if listErr != nil {
			http.Error(w, "approval state unavailable", http.StatusInternalServerError)
			return
		}
		for _, pending := range values {
			if pending.ID == chi.URLParam(r, "approvalID") && len(pending.Decisions) > 0 {
				pinned := pending.Decisions[0].ArtifactRevisions
				pinnedSelection = true
				selectedRevisions = make(map[string]string, len(pinned))
				for path, revisionID := range pinned {
					selectedRevisions[path] = revisionID
				}
				break
			}
		}
	}
	if len(selectedRevisions) == 0 && !pinnedSelection {
		attemptID, attemptErr := s.approvalAttemptID(chi.URLParam(r, "runID"), chi.URLParam(r, "approvalID"))
		if attemptErr != nil {
			http.Error(w, "не удалось определить артефакты approval", http.StatusInternalServerError)
			return
		}
		revisionSelection, revisionErr := s.latestAttemptRevisions(chi.URLParam(r, "runID"), attemptID)
		if revisionErr != nil {
			http.Error(w, "не удалось закрепить текущие версии артефактов", http.StatusConflict)
			return
		}
		selectedRevisions = revisionSelection
	}
	value, err := s.controller.Decide(
		chi.URLParam(r, "runID"), chi.URLParam(r, "approvalID"),
		approval.Decision{
			ActorID: actorID, ActorRole: actorRole,
			Action: command.Action, Comment: command.Comment,
			SubjectHash:             command.SubjectHash,
			ArtifactRevisions:       selectedRevisions,
			ControllerAuthenticated: s.authenticator != nil,
		},
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSONResponse(w, http.StatusOK, value)
}

func (s *Server) approvalAttemptID(runID, approvalID string) (string, error) {
	values, err := s.controller.Approvals(runID)
	if err != nil {
		return "", err
	}
	for _, value := range values {
		if value.ID == approvalID {
			return value.AttemptID, nil
		}
	}
	return "", nil
}

func (s *Server) latestAttemptRevisions(runID, attemptID string) (map[string]string, error) {
	if !safeIdentity(runID) || !safeIdentity(attemptID) {
		return nil, nil
	}
	_, manifest, err := evidence.ReadAttemptManifest(nil, filepath.Join(s.runRoot, runID), runID, attemptID)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if manifest.RunID != runID || manifest.AttemptID != attemptID {
		return nil, errors.New("approval attempt manifest identity mismatch")
	}
	selection := make(map[string]string)
	for _, output := range manifest.Outputs {
		revisions, listErr := s.humanArtifacts.List(runID, output.EvidencePath)
		if listErr != nil {
			return nil, listErr
		}
		if len(revisions) > 0 {
			selection[output.EvidencePath] = revisions[len(revisions)-1].ID
		}
	}
	return selection, nil
}

// approvalByID returns the approval with id from values.
func approvalByID(values []approval.PendingApproval, id string) (approval.PendingApproval, bool) {
	for _, value := range values {
		if value.ID == id {
			return value, true
		}
	}
	return approval.PendingApproval{}, false
}

func containsApprovalRole(roles []string, expected string) bool {
	for _, role := range roles {
		if role == expected {
			return true
		}
	}
	return false
}

func decodeCommand(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxCommandBody)
	return strictjson.Decode(r.Body, destination)
}

func requireEmptyCommand(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxCommandBody)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) != "" && strings.TrimSpace(string(data)) != "{}" {
		return errors.New("command body должен быть пустым")
	}
	return nil
}

func writeJSONResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
