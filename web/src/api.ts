import type { CreateTaskRequest, PipelineRun, Stage, StageOwner, Artifact, Approval, ArtifactRevision, DeliveryProjection, LogTail, PreflightReport, TaskRunDetails, TaskRunEventsResponse, TaskRunFilters, TaskRunsResponse, TemplatesResponse, WorkflowSnapshot, Principal, ProjectTemplateResponse, TemplateValidation, TemplateVersion } from './types';
import type { FlowMetrics } from './types/flowMetrics';

const API_BASE = '/api';

export const SESSION_EXPIRED_EVENT = 'ai-team:session-expired';

function expireSession() {
  csrfToken = null;
  activePrincipal = null;
  if (typeof window !== 'undefined') window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
}

async function responseError(response: Response, context = 'API error'): Promise<Error> {
  const raw = (await response.text()).trim();
  let message = raw;
  if (response.headers.get('content-type')?.includes('application/json')) {
    try {
      const parsed = JSON.parse(raw) as { error?: string; allowed_actions?: string[] };
      if (parsed.error) {
        message = parsed.error;
        if (Array.isArray(parsed.allowed_actions) && parsed.allowed_actions.length > 0) {
          message += ` Доступные действия: ${parsed.allowed_actions.join(', ')}.`;
        }
      }
    } catch {
      // keep raw body
    }
  }
  if (response.status === 401) {
    expireSession();
    return new Error('Сессия истекла. Войдите снова.');
  }
  return new Error(message || `${context}: ${response.status}`);
}

async function fetchJson<T>(url: string): Promise<T> {
  const res = await fetch(`${API_BASE}${url}`, { credentials: 'same-origin' });
  if (!res.ok) {
    throw await responseError(res);
  }
  return res.json();
}

export async function getPipelineRuns(limit = 100, offset = 0): Promise<PipelineRun[]> {
  return fetchJson<PipelineRun[]>(`/pipelines?limit=${limit}&offset=${offset}`);
}

export async function getPipelineRun(id: number): Promise<{ run: PipelineRun; stages: Stage[]; approvals?: Approval[]; next_stage?: string; stage_owners?: Record<string, StageOwner>; delivery?: DeliveryProjection }> {
  return fetchJson(`/pipelines/${id}`);
}

export async function getTaskRun(runID: string): Promise<{ run: PipelineRun; stages: Stage[] }> {
  return fetchJson(`/runs/${encodeURIComponent(runID)}`);
}

// getGlobalEvents pages the global feed newest-first. `before` is the previous
// page's next_cursor; 0 omits the parameter and starts from the newest
// persisted event (the server rejects an explicit before=0).
export async function getGlobalEvents(before = 0, limit = 100, warningsOnly = false): Promise<TaskRunEventsResponse> {
  const query = new URLSearchParams({ order: 'desc', limit: String(limit) });
  if (before > 0) query.set('before', String(before));
  if (warningsOnly) query.set('warnings', 'true');
  return fetchJson(`/events?${query.toString()}`);
}

let csrfToken: string | null = null;
let activePrincipal: Principal | null = null;

export async function getAuthConfig(): Promise<{ authentication_required: boolean; team_management_enabled: boolean; sso_enabled?: boolean }> {
  const response = await fetch(`${API_BASE}/auth/config`, { credentials: 'same-origin' });
  if (!response.ok) throw new Error(`Auth config error: ${response.status}`);
  return response.json();
}

export async function getCurrentIdentity(): Promise<{ authentication_required: boolean; principal?: Principal }> {
  const response = await fetch(`${API_BASE}/auth/me`, { credentials: 'same-origin' });
  if (!response.ok) throw new Error(`Identity error: ${response.status}`);
  const value = await response.json() as { authentication_required: boolean; principal?: Principal };
  activePrincipal = value.principal ?? null;
  return value;
}

export async function openSession(bearerToken?: string): Promise<Principal | null> {
  const headers: Record<string, string> = {};
  if (bearerToken) headers.Authorization = `Bearer ${bearerToken}`;
  const response = await fetch(`${API_BASE}/session`, {
    credentials: 'same-origin',
    headers,
  });
  if (!response.ok) {
    if (bearerToken) {
      csrfToken = null;
      activePrincipal = null;
      throw new Error('Не удалось войти. Проверьте access token.');
    }
    throw await responseError(response, 'Authentication failed');
  }
  const session = await response.json() as { csrf_token: string; principal?: Principal };
  csrfToken = session.csrf_token;
  activePrincipal = session.principal ?? null;
  return activePrincipal;
}

export const SSO_LOGIN_URL = `${API_BASE}/auth/oidc/login`;

// closeSession revokes the server-side session (sign-out). The CSRF token is
// required, so another site cannot sign the user out.
export async function closeSession(): Promise<void> {
  const token = csrfToken;
  if (token) {
    await fetch(`${API_BASE}/session`, {
      method: 'DELETE', credentials: 'same-origin', headers: { 'X-CSRF-Token': token },
    });
  }
  expireSession();
}

export function getActivePrincipal(): Principal | null {
  return activePrincipal;
}

export type TeamMember = { actor_id: string; email: string; roles: string[]; status: 'invited' | 'active' | 'revoked'; updated_at: string };
export type TeamAuditEvent = { id: number; actor_id: string; action: string; target_actor_id: string; details: unknown; created_at: string };

export function getTeamMembers(): Promise<{ members: TeamMember[] }> { return fetchJson('/team/members'); }
export function getTeamAudit(): Promise<{ events: TeamAuditEvent[] }> { return fetchJson('/team/audit'); }
export function inviteTeamMember(email: string, roles: string[]): Promise<{ actor_id: string; activation_token: string; expires_at: string }> {
  return command('/team/invitations', { email, roles });
}

async function teamMutation<T>(url: string, method: 'PATCH' | 'DELETE', body?: unknown): Promise<T> {
  const token = await getCsrfToken();
  const response = await fetch(`${API_BASE}${url}`, { method, credentials: 'same-origin', headers: {
    'Content-Type': 'application/json', 'X-CSRF-Token': token,
  }, body: body === undefined ? undefined : JSON.stringify(body) });
  if (!response.ok) throw await responseError(response);
  return response.json();
}

export function setTeamMemberRoles(actorId: string, roles: string[]): Promise<{ member: TeamMember }> {
  return teamMutation(`/team/members/${encodeURIComponent(actorId)}/roles`, 'PATCH', { roles });
}
export function revokeTeamMember(actorId: string): Promise<{ actor_id: string; status: string }> {
  return teamMutation(`/team/members/${encodeURIComponent(actorId)}`, 'DELETE');
}

export async function activateTeamInvitation(token: string): Promise<{ access_token: string }> {
  const response = await fetch(`${API_BASE}/team/activate`, { method: 'POST', credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ token }) });
  if (!response.ok) throw await responseError(response, 'Activation failed');
  return response.json();
}

async function getCsrfToken(): Promise<string> {
  if (csrfToken) return csrfToken;
  await openSession();
  if (!csrfToken) throw new Error('Session did not provide CSRF token');
  return csrfToken;
}

async function command<T>(url: string, body?: unknown, extraHeaders?: Record<string, string>): Promise<T> {
  const token = await getCsrfToken();
  const response = await fetch(`${API_BASE}${url}`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json',
      'X-CSRF-Token': token,
      ...extraHeaders,
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!response.ok) {
    throw await responseError(response);
  }
  return response.json();
}

export function startRun(feature: string, task: string): Promise<{ run_id: string }> {
  return command('/runs', { feature, task });
}

export function resumeRun(runId: string): Promise<{ run_id: string }> {
  return command(`/runs/${encodeURIComponent(runId)}/resume`);
}

export function retryDelivery(runId: string): Promise<{ run_id: string; delivery: NonNullable<DeliveryProjection['record']> }> {
  return command(`/runs/${encodeURIComponent(runId)}/delivery/retry`);
}

export function cancelRun(runId: string): Promise<{ run_id: string }> {
  return command(`/runs/${encodeURIComponent(runId)}/cancel`);
}

export function takeStage(runId: string, stageId: string): Promise<{ owner: StageOwner; changed: boolean }> {
  return command(`/runs/${encodeURIComponent(runId)}/stages/${encodeURIComponent(stageId)}/take`);
}

export function decideApproval(
  runId: string,
  value: Approval,
  decision: { actor_id: string; actor_role: string; action: string; comment?: string; artifact_revisions?: Record<string, string> },
): Promise<Approval> {
  return command(
    `/runs/${encodeURIComponent(runId)}/approvals/${encodeURIComponent(value.id)}/decisions`,
    { ...decision, subject_hash: value.subject_hash },
  );
}

export async function getPipelineArtifacts(id: number): Promise<Artifact[]> {
  return fetchJson(`/pipelines/${id}/artifacts`);
}

export function getPreflight(): Promise<PreflightReport> {
  return fetchJson('/preflight');
}

export function getRunLog(runId: string, attemptId: string): Promise<LogTail> {
  return fetchJson(`/runs/${encodeURIComponent(runId)}/logs/${encodeURIComponent(attemptId)}`);
}

export function getRunWorkflow(runId: string): Promise<WorkflowSnapshot> {
  return fetchJson(`/runs/${encodeURIComponent(runId)}/workflow`);
}

export function getProjectTemplate(): Promise<ProjectTemplateResponse> {
  return fetchJson('/template');
}

export function getTemplateVersions(): Promise<{ versions: TemplateVersion[] }> {
  return fetchJson('/template/versions');
}

export function validateProjectTemplate(yaml: string): Promise<TemplateValidation> {
  return command('/template/validate', { yaml });
}

export function publishProjectTemplate(yaml: string, expectedVersion: string): Promise<{ version: string }> {
  return command('/template/publish', { yaml, expected_version: expectedVersion });
}

export function getRunTemplateVersion(runId: string): Promise<{ version: string }> {
  return fetchJson(`/runs/${encodeURIComponent(runId)}/template-version`);
}

// Содержимое артефакта: сервер отдаёт raw text/markdown (не JSON).
// path — относительный путь; слэши сохраняются, сегменты кодируются.
export async function getArtifact(runId: string, path: string): Promise<string> {
  const encodedRun = encodeURIComponent(runId);
  const encoded = path.split('/').map(encodeURIComponent).join('/');
  const res = await fetch(`${API_BASE}/runs/${encodedRun}/artifacts/${encoded}`);
  if (!res.ok) {
    throw new Error(`API error: ${res.status}`);
  }
  return res.text();
}

export async function getArtifactRevisions(runId: string, path: string): Promise<ArtifactRevision[]> {
  const encodedRun = encodeURIComponent(runId);
  const response = await fetchJson<{ revisions: ArtifactRevision[] }>(
    `/runs/${encodedRun}/artifact-revisions?path=${encodeURIComponent(path)}`,
  );
  return response.revisions ?? [];
}

export function createArtifactRevision(
  runId: string,
  value: { artifact_path: string; base_revision?: string; base_sha256: string; content: string; comment?: string },
): Promise<ArtifactRevision> {
  return command(`/runs/${encodeURIComponent(runId)}/artifact-revisions`, value);
}

// GET /api/runs: фильтрованный список задач (этап, функция, состояние, режим).
export function getTaskRuns(filters: TaskRunFilters = {}): Promise<TaskRunsResponse> {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(filters)) {
    if (value !== undefined && value !== null && value !== '') query.set(key, String(value));
  }
  if (!query.has('limit')) query.set('limit', '50');
  if (!query.has('offset')) query.set('offset', '0');
  return fetchJson(`/runs?${query.toString()}`);
}

export function getTaskRunDetails(runId: string): Promise<TaskRunDetails> {
  return fetchJson(`/runs/${encodeURIComponent(runId)}`);
}

export function getTaskRunEvents(runId: string, after = 0, limit = 1000): Promise<TaskRunEventsResponse> {
  const query = new URLSearchParams({ after: String(after), limit: String(limit) });
  return fetchJson(`/runs/${encodeURIComponent(runId)}/events?${query.toString()}`);
}

export function takeTaskStage(runId: string, stageId: string): Promise<unknown> {
  return command(`/runs/${encodeURIComponent(runId)}/stages/${encodeURIComponent(stageId)}/take`);
}

export function runTaskStageAgent(runId: string, stageId: string, action: 'run' | 'refine' = 'run'): Promise<unknown> {
  return command(`/runs/${encodeURIComponent(runId)}/stages/${encodeURIComponent(stageId)}/run-agent`, { action });
}

export function setTaskStageExecutor(runId: string, stageId: string, executor: 'human' | 'agent'): Promise<unknown> {
  return command(`/runs/${encodeURIComponent(runId)}/stages/${encodeURIComponent(stageId)}/executor`, { executor });
}

export function submitTaskStageResult(runId: string, stageId: string, value: {
  result: 'md' | 'link' | 'approve'; content?: string; url?: string;
  link_kind?: 'pr' | 'build' | 'other'; note?: string; description?: string;
}): Promise<unknown> {
  return command(`/runs/${encodeURIComponent(runId)}/stages/${encodeURIComponent(stageId)}/results`, value);
}

export function confirmTaskStage(runId: string, stageId: string): Promise<unknown> {
  return command(`/runs/${encodeURIComponent(runId)}/stages/${encodeURIComponent(stageId)}/confirm`);
}

export function overrideTaskStageCheck(runId: string, stageId: string, reason: string): Promise<unknown> {
  return command(`/runs/${encodeURIComponent(runId)}/stages/${encodeURIComponent(stageId)}/override`, { reason });
}

export function skipTaskStage(runId: string, stageId: string, reason: string): Promise<unknown> {
  return command(`/runs/${encodeURIComponent(runId)}/stages/${encodeURIComponent(stageId)}/skip`, { reason });
}

export function returnTaskStage(runId: string, stageId: string, toStage: string, reason: string): Promise<unknown> {
  return command(`/runs/${encodeURIComponent(runId)}/stages/${encodeURIComponent(stageId)}/return`, { to_stage: toStage, reason });
}

export function answerTaskGrill(runId: string, stageId: string, answer: string): Promise<unknown> {
  return command(`/runs/${encodeURIComponent(runId)}/stages/${encodeURIComponent(stageId)}/grill/answer`, { answer });
}

export function finishTaskGrill(runId: string, stageId: string): Promise<unknown> {
  return command(`/runs/${encodeURIComponent(runId)}/stages/${encodeURIComponent(stageId)}/grill/done`);
}

export function approveTaskDeliveryPlan(runId: string, planHash: string): Promise<unknown> {
  return command(`/runs/${encodeURIComponent(runId)}/plan/approve`, { plan_hash: planHash });
}

// GET /api/templates: активный шаблон проекта и его этапы для доски.
export function getTemplatesList(): Promise<TemplatesResponse> {
  return fetchJson('/templates');
}

// POST /api/runs: создание задачи. Idempotency-Key защищает от двойного
// создания при повторе сетевого сбоя — ключ передаёт вызывающий код.
export function createTask(payload: CreateTaskRequest, idempotencyKey: string): Promise<{ run_id: string; template_version?: string }> {
  return command('/runs', payload, { 'Idempotency-Key': idempotencyKey });
}

function flowMetricsQuery(from: string, to: string): string {
  const query = new URLSearchParams({ from, to });
  return query.toString();
}

export function getFlowMetrics(from: string, to: string): Promise<FlowMetrics> {
  return fetchJson(`/metrics/flow?${flowMetricsQuery(from, to)}`);
}

// Агрегаты потока (токены, оценка стоимости подписки, раунды) по этапам одной
// задачи — тот же расчёт, что и на странице наблюдаемости.
export function getTaskFlowMetrics(runId: string): Promise<FlowMetrics> {
  return fetchJson(`/metrics/flow?${new URLSearchParams({ run_id: runId }).toString()}`);
}

export async function downloadFlowMetricsCsv(from: string, to: string): Promise<Blob> {
  const response = await fetch(`${API_BASE}/metrics/flow.csv?${flowMetricsQuery(from, to)}`, { credentials: 'same-origin' });
  if (!response.ok) throw await responseError(response, 'Не удалось экспортировать метрики');
  return response.blob();
}
