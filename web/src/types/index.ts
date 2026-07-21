// Статусы синхронизированы с backend:
// run: running/completed/failed/blocked/stopped (pkg/pipeline.runStatus)
// stage: running/passed/failed/blocked (pkg/notifier.Status*)
export type PipelineStatus = 'running' | 'completed' | 'completed_with_warnings' | 'failed' | 'blocked' | 'stopped' | 'canceled' | 'interrupted';

export type StageStatus = 'running' | 'passed' | 'failed' | 'blocked' | 'rejected' | 'canceled' | 'warning' | 'skipped' | 'invalidated';

export interface PipelineRun {
  id: number;
  run_id: string;
  feature: string;
  status: PipelineStatus;
  started_at: string;
  completed_at?: string;
  config_snapshot?: string;
}

export interface Stage {
  id: number;
  pipeline_run_id: number;
  attempt_id: string;
  stage_index: number;
  agent_name: string;
  status: StageStatus;
  started_at: string;
  completed_at?: string;
  duration_ms?: number;
  error?: string;
  verdict?: string;
  execution?: string;
  decision?: string;
  outcome?: string;
  inputs_json?: string;
  outputs_json?: string;
}

// Ответ GET /api/pipelines/{id}/artifacts; path — относительный к корню
// артефактов, он же аргумент для getArtifact().
export interface Artifact {
  name: string;
  path: string;
  run_id: string;
  size: number;
  mod_time: string;
}

// JSON события из pkg/web/websocket.go (поля: agent, status)
export interface WsEvent {
  type: 'stage_started' | 'stage_completed' | 'pipeline_completed';
  pipeline_id: number;
  agent?: string;
  status?: string;
  duration_ms?: number;
}
