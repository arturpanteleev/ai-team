import type { PipelineRun, Stage, Artifact, Approval, LogTail, PreflightReport, WorkflowSnapshot, Principal } from './types';

const API_BASE = '/api';

async function fetchJson<T>(url: string): Promise<T> {
  const res = await fetch(`${API_BASE}${url}`, { credentials: 'same-origin' });
  if (!res.ok) {
    throw new Error(`API error: ${res.status}`);
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
  if (!response.ok) throw new Error(`Authentication failed: ${response.status}`);
  const session = await response.json() as { csrf_token: string; principal?: Principal };
  csrfToken = session.csrf_token;
  activePrincipal = session.principal ?? null;
  return activePrincipal;
}

export function getActivePrincipal(): Principal | null {
  return activePrincipal;
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
    const message = await response.text();
    throw new Error(message.trim() || `API error: ${response.status}`);
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
  decision: { actor_id: string; actor_role: string; action: string; comment?: string },
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
