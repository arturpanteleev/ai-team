import type { PipelineRun, Stage, Artifact, ArtifactContent } from './types';

const API_BASE = '/api';

async function fetchJson<T>(url: string): Promise<T> {
  const res = await fetch(`${API_BASE}${url}`);
  if (!res.ok) {
    throw new Error(`API error: ${res.status}`);
  }
  return res.json();
}

export async function getPipelineRuns(): Promise<PipelineRun[]> {
  return fetchJson<PipelineRun[]>('/pipelines');
}

export async function getPipelineRun(id: number): Promise<{ run: PipelineRun; stages: Stage[] }> {
  return fetchJson(`/pipelines/${id}`);
}

export async function getPipelineArtifacts(id: number): Promise<Artifact[]> {
  return fetchJson(`/pipelines/${id}/artifacts`);
}

export async function getArtifact(path: string): Promise<ArtifactContent> {
  const encoded = encodeURIComponent(path);
  return fetchJson(`/artifacts/${encoded}`);
}