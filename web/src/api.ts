import type { PipelineRun, Stage, Artifact, Approval, ArtifactRevision, LogTail, PreflightReport, WorkflowSnapshot, Principal } from './types';

const API_BASE = '/api';

export const SESSION_EXPIRED_EVENT = 'ai-team:session-expired';

function expireSession() {
  csrfToken = null;
  activePrincipal = null;
  if (typeof window !== 'undefined') window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
}

async function responseError(response: Response, context = 'API error'): Promise<Error> {
  const message = (await response.text()).trim();
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

export async function getPipelineRun(id: number): Promise<{ run: PipelineRun; stages: Stage[]; approvals?: Approval[]; next_stage?: string }> {
  return fetchJson(`/pipelines/${id}`);
}

let csrfToken: string | null = null;
let activePrincipal: Principal | null = null;

export async function getAuthConfig(): Promise<{ authentication_required: boolean }> {
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

async function command<T>(url: string, body?: unknown): Promise<T> {
  const token = await getCsrfToken();
  const response = await fetch(`${API_BASE}${url}`, {
    method: 'POST',
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json',
      'X-CSRF-Token': token,
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

export function cancelRun(runId: string): Promise<{ run_id: string }> {
  return command(`/runs/${encodeURIComponent(runId)}/cancel`);
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
