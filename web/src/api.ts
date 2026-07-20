import type { PipelineRun, Stage, Artifact } from './types';

const API_BASE = '/api';

async function fetchJson<T>(url: string): Promise<T> {
  const res = await fetch(`${API_BASE}${url}`);
  if (!res.ok) {
    throw new Error(`API error: ${res.status}`);
  }
  return res.json();
}

export async function getPipelineRuns(limit = 100, offset = 0): Promise<PipelineRun[]> {
  return fetchJson<PipelineRun[]>(`/pipelines?limit=${limit}&offset=${offset}`);
}

export async function getPipelineRun(id: number): Promise<{ run: PipelineRun; stages: Stage[] }> {
  return fetchJson(`/pipelines/${id}`);
}

export async function getPipelineArtifacts(id: number): Promise<Artifact[]> {
  return fetchJson(`/pipelines/${id}/artifacts`);
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
