export type PipelineStatus = 'running' | 'completed' | 'failed' | 'blocked' | 'pending';

export type StageStatus = 'running' | 'completed' | 'failed' | 'skipped' | 'pending';

export interface PipelineRun {
  id: number;
  feature: string;
  task: string;
  status: PipelineStatus;
  started_at: string;
  completed_at?: string;
  error?: string;
}

export interface Stage {
  id: number;
  pipeline_run_id: number;
  agent_name: string;
  status: StageStatus;
  started_at: string;
  completed_at?: string;
  duration_ms?: number;
  error?: string;
}

export interface Artifact {
  name: string;
  path: string;
  mime_type: string;
}

export interface ArtifactContent {
  content: string;
  path: string;
}

export interface WsEvent {
  type: 'stage_started' | 'stage_completed' | 'pipeline_completed';
  pipeline_id: number;
  agent_name?: string;
  status?: StageStatus;
  pipeline_status?: PipelineStatus;
}