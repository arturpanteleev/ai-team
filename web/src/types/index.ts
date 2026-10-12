// Статусы синхронизированы с backend:
// run: running/completed/failed/blocked/stopped (pkg/pipeline.runStatus)
// stage: running/passed/failed/blocked (pkg/notifier.Status*)
export type PipelineStatus = 'queued' | 'running' | 'waiting_for_approval' | 'completed' | 'completed_with_warnings' | 'failed' | 'blocked' | 'stopped' | 'canceled' | 'interrupted';

export type StageStatus = 'running' | 'passed' | 'failed' | 'blocked' | 'rejected' | 'canceled' | 'warning' | 'skipped' | 'invalidated';

export type CloudRole = 'product_owner' | 'architect' | 'developer' | 'reviewer' | 'qa' | 'release_manager';

export interface Principal {
  actor_id: string;
  roles: CloudRole[];
}

export interface PipelineRun {
  id: number;
  run_id: string;
  feature: string;
  status: PipelineStatus;
  started_at: string;
  completed_at?: string;
  config_snapshot?: string;
  queue_job_id?: number;
  error?: string;
}

export interface DeliveryProjection {
  status: 'not_requested' | 'pending' | 'failed' | 'recorded' | 'unavailable';
  record?: {
    schema_version: number;
    run_id: string;
    feature: string;
    plan_hash: string;
    commit_sha?: string;
    pr_url?: string;
    performed_at: string;
  };
  error?: string;
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
  checks_json?: string;
  mutations_json?: string;
  delivery_json?: string;
}

export interface StageOwner {
  stage_id: string;
  approval_id: string;
  actor_id: string;
  actor_role: string;
  taken_at: string;
}

export interface CheckEvidence {
  name: string;
  class: string;
  command: string[];
  policy: string;
  status: string;
  duration_ns?: number;
  exit_code: number;
  reason?: string;
}

export interface DeliveryStep {
  step: string;
  command?: string[];
  status: string;
  exit_code: number;
  reason?: string;
}

export interface DeliveryEvidence {
  status?: string;
  pr_url?: string;
  steps?: DeliveryStep[];
}

export interface LogTail {
  run_id: string;
  attempt_id: string;
  offset: number;
  truncated: boolean;
  content: string;
}

export interface PreflightCheck {
  id: string;
  status: 'passed' | 'warning' | 'failed';
  required: boolean;
  message: string;
}

export interface PreflightReport {
  ready: boolean;
  readiness?: 'ready' | 'blocked' | 'unknown';
  unknown?: boolean;
  checked_at: string;
  checks?: PreflightCheck[] | null;
}

export interface WorkflowApproval {
  roles: string[];
  quorum: 'any' | 'all';
  actions: Record<string, string>;
}

export interface WorkflowNode {
  name: string;
  max_visits?: number;
}

export interface WorkflowEdge {
  from: string;
  outcome: string;
  to: string;
  approval?: WorkflowApproval;
}

export interface WorkflowGraph {
  schema_version: number;
  entry: string;
  nodes: WorkflowNode[];
  edges: WorkflowEdge[];
}

export interface WorkflowSnapshot {
  schema_version: number;
  graph?: WorkflowGraph;
}

export interface ProjectTemplateStage {
  id: string;
  title: string;
  function: string;
  result: 'md' | 'link' | 'approve';
  executor: 'human' | 'agent';
  agent?: string;
  max_visits?: number;
}

export interface ProjectTemplateReturn {
  from: string;
  to: string;
  max_visits?: number;
}

export interface TemplateVersion {
  id: string;
  created_at?: string;
}

export interface TemplateValidation {
  valid: boolean;
  diagnostic?: string;
  template?: string;
  title?: string;
  version?: string;
  yaml?: string;
  graph?: WorkflowGraph;
  stages?: ProjectTemplateStage[];
  returns?: ProjectTemplateReturn[];
}

export interface ProjectTemplateResponse {
  template: TemplateValidation;
  versions: TemplateVersion[];
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

export interface ApprovalDecision {
  approval_id: string;
  actor_id: string;
  actor_role: string;
  action: string;
  comment?: string;
  subject_hash: string;
  artifact_revisions?: Record<string, string>;
  candidate_sha256?: string;
  decided_at: string;
}

export interface Approval {
  id: string;
  run_id: string;
  attempt_id: string;
  from_stage: string;
  to_stage: string;
  trigger: string;
  subject_hash: string;
  candidate_sha256?: string;
  required_roles: string[];
  quorum: 'any' | 'all';
  actions: string[];
  feedback_actions?: string[];
  targets?: Record<string, string>;
  payload?: unknown;
  status: 'pending' | 'resolved';
  decisions?: ApprovalDecision[];
  resolved_action?: string;
  artifact_revisions?: Record<string, string>;
  artifact_revision_binding_sha256?: string;
  created_at: string;
  resolved_at?: string;
}

export interface ArtifactRevision {
  revision: number;
  id: string;
  base_revision?: string;
  base_sha256?: string;
  content: string;
  comment?: string;
  actor_id: string;
  created_at: string;
  sha256: string;
}

export interface TaskEvent {
  version: 1;
  cursor: number;
  run_id: string;
  sequence: number;
  type: string;
  stage?: string;
  actor?: string;
  executor?: string;
  severity?: string;
  attempt_id?: string;
  timestamp: string;
  data: Record<string, unknown>;
}

export interface WsEvent extends TaskEvent {
  stream?: string;
}

// Элемент GET /api/runs: карточка доски/списка задач (B-70 task API).
export interface TaskRun {
  id: number;
  run_id: string;
  feature: string;
  task?: string;
  status: PipelineStatus;
  phase?: string;
  current_stage?: string;
  current_function?: string;
  executor?: 'human' | 'agent';
  executor_agent?: string;
  mode?: 'manual' | 'auto';
  allowed_actions: string[];
  started_at: string;
  completed_at?: string;
  stage_entered_at?: string;
  warnings: number;
  external_links?: string[];
}

export interface TaskRunsResponse {
  runs: TaskRun[];
  total: number;
  limit: number;
  offset: number;
}

export interface TaskStageTemplate {
  id: string;
  title: string;
  function: string;
  result: 'md' | 'link' | 'approve';
  executor: 'human' | 'agent';
  agent?: string;
  link_kind?: 'pr' | 'build' | 'other';
  skippable?: boolean;
  max_visits?: number;
}

export interface TaskTemplateReturn {
  from: string;
  to: string;
  max_visits?: number;
}

export interface PinnedTaskTemplate {
  valid: boolean;
  template: string;
  title: string;
  version: string;
  stages: TaskStageTemplate[];
  returns: TaskTemplateReturn[];
  graph?: WorkflowGraph;
}

export interface TaskLifecycleState {
  run_id: string;
  task: string;
  phase: 'running' | 'waiting' | 'resumable' | 'terminal';
  next_stage?: string;
  pending_approval_id?: string;
  attempt_ordinal: number;
  executor_overrides?: Record<string, {
    executor: 'human' | 'agent';
    previous_executor: 'human' | 'agent';
  }>;
}

export interface TaskResultRevision {
  run_id: string;
  artifact_path: string;
  revision: number;
  id: string;
  base_revision?: string;
  base_sha256?: string;
  content?: string;
  comment?: string;
  description?: string;
  actor_id: string;
  stage_id?: string;
  approval_id?: string;
  result?: 'md' | 'link' | 'approve';
  link_kind?: 'pr' | 'build' | 'other';
  created_at: string;
  sha256: string;
}

export interface TaskRunDetails {
  run: PipelineRun;
  task?: string;
  mode?: 'manual' | 'auto';
  external_links?: string[];
  template?: PinnedTaskTemplate;
  state?: TaskLifecycleState;
  stages: Stage[];
  approvals: Approval[];
  stage_owners: Record<string, StageOwner>;
  results: Record<string, TaskResultRevision[]>;
  allowed_actions: string[];
  delivery: DeliveryProjection;
}

export interface TaskRunEventsResponse {
  events: TaskEvent[];
  next_cursor: number;
}

export interface TaskRunFilters {
  status?: string;
  stage?: string;
  function?: string;
  mode?: string;
  limit?: number;
  offset?: number;
}

export interface TemplatesResponse {
  templates: TemplateValidation[];
  active_template: string;
  versions: TemplateVersion[];
}

export interface CreateTaskRequest {
  feature: string;
  task?: string;
  title?: string;
  idea?: string;
  template?: string;
  mode?: 'manual' | 'auto';
  external_links?: string[];
}
