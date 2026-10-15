// Статусы синхронизированы с backend:
// run: running/completed/failed/blocked/stopped (pkg/pipeline.runStatus)
// stage: running/passed/failed/blocked (pkg/notifier.Status*)
export type PipelineStatus = 'queued' | 'running' | 'waiting_for_approval' | 'completed' | 'completed_with_warnings' | 'failed' | 'blocked' | 'stopped' | 'canceled' | 'interrupted';

export type StageStatus = 'running' | 'passed' | 'failed' | 'blocked' | 'rejected' | 'canceled' | 'warning' | 'skipped' | 'invalidated';

export type CloudRole = 'product_owner' | 'architect' | 'developer' | 'reviewer' | 'qa' | 'release_manager' | 'admin';

export interface Principal {
  actor_id: string;
  roles: CloudRole[];
}

export interface PipelineRun {
  id: number;
  run_id: string;
  project_id?: number;
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
  /** Optional confirming function; confirm/return require it when set (B-82). */
  approver_function?: string;
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
  kind?: string;
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
  // Серверная классификация предупреждения (store.WarningEventTypes или
  // severity=warning); клиент не держит свой список типов.
  warning?: boolean;
  attempt_id?: string;
  timestamp: string;
  data: Record<string, unknown>;
}

export interface WsEvent extends TaskEvent {
  stream?: string;
}

// Элемент GET /api/runs: карточка доски/списка задач (B-70 task API).
// Вычисляемые статусы этапов (S11). Сервер выводит их из журнала событий,
// клиент их только показывает: записи статуса в API нет.
export type StageStatusKey = 'not_reached' | 'ready' | 'in_progress' | 'checking' | 'awaiting_human'
  | 'passed' | 'rejected' | 'failed' | 'blocked' | 'skipped' | 'canceled';
export type StageReadiness = 'not_reached' | 'ready' | 'engaged' | 'left';
export type StagePhase = 'none' | 'working' | 'checking' | 'awaiting_human';
export type StageExecutorKind = 'human' | 'internal_agent' | 'external_agent' | 'unknown';

export interface StageStatusExecutor {
  kind: StageExecutorKind;
  name?: string;
  agent?: string;
  actor?: string;
  // attempt — фактический исполнитель из доказательств попытки; configured — по шаблону; override — выбор человека.
  source: 'attempt' | 'configured' | 'override';
}

export interface StageStatusBlock {
  kind: string;
  reason?: string;
  approval_id?: string;
  roles?: string[];
  gates?: string[];
  actions?: string[];
}

export interface StageVisitAttempt {
  attempt_id: string;
  executor: StageStatusExecutor;
  execution?: string;
  outcome: string;
  verdict?: string;
  superseded?: boolean;
  started_at: string;
  finished_at?: string;
}

export interface StageVisit {
  number: number;
  active: boolean;
  entered_at: string;
  entered_from?: string;
  entered_via: 'start' | 'transition' | 'return' | 'inferred';
  left_at?: string;
  left_to?: string;
  left_action?: string;
  returned?: boolean;
  outcome: string;
  superseded?: boolean;
  overridden?: boolean;
  skipped?: boolean;
  warnings?: number;
  executor: StageStatusExecutor;
  attempts: StageVisitAttempt[];
}

export interface StageStatusEntry {
  stage_id: string;
  title?: string;
  function?: string;
  index: number;
  status: StageStatusKey;
  readiness: StageReadiness;
  phase: StagePhase;
  outcome: string;
  executor: StageStatusExecutor;
  current: boolean;
  visited: boolean;
  visit_count: number;
  active_visit: number;
  visits: StageVisit[];
  overridden?: boolean;
  warnings?: number;
  blocked_by?: StageStatusBlock;
  allowed_actions: string[];
  action_stage_id?: string;
}

export interface StageStatusProjection {
  schema_version: number;
  run_status?: string;
  terminal: boolean;
  current_stage?: string;
  stages: StageStatusEntry[];
}

export interface StageRouteItem {
  stage_id: string;
  status: StageStatusKey;
  readiness: StageReadiness;
  visited: boolean;
  visit_count: number;
  current?: boolean;
}

// Сжатая проекция для карточки в списке: текущий этап и маршрут.
export interface StageStatusSummary {
  schema_version: number;
  terminal: boolean;
  current_stage?: string;
  status?: StageStatusKey;
  readiness?: StageReadiness;
  phase?: StagePhase;
  outcome?: string;
  executor?: StageStatusExecutor;
  visit?: number;
  visit_count?: number;
  returned?: boolean;
  blocked_by?: StageStatusBlock;
  route: StageRouteItem[];
}

export interface TaskRun {
  id: number;
  run_id: string;
  project_id?: number;
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
  stage_status?: StageStatusSummary;
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
  /** Optional confirming function; confirm and return belong to it when set. */
  approver_function?: string;
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
  /** Template forbids confirming a stage you took or submitted (B-82). */
  forbid_self_approval?: boolean;
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
  // The part of allowed_actions the requesting principal may perform (stage
  // function, administrator right, plan roles). Absent from an older server.
  viewer_actions?: string[];
  delivery: DeliveryProjection;
  stage_statuses?: StageStatusProjection;
}

export interface TaskRunEventsResponse {
  events: TaskEvent[];
  next_cursor: number;
  // Есть ли ещё подходящие события за next_cursor (с учётом фильтра).
  has_more?: boolean;
}

export interface TaskRunFilters {
  project_id?: number | string;
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
  project_id: number;
  feature: string;
  task?: string;
  title?: string;
  idea?: string;
  template?: string;
  mode?: 'manual' | 'auto';
  external_links?: string[];
}

/** Проект — папка для группировки задач; архивный не принимает новые задачи. */
export interface Project {
  id: number;
  name: string;
  description: string;
  archived: boolean;
  archived_at?: string;
  created_by?: string;
  created_at: string;
  updated_at: string;
}

// Gate state of a task (S06): GET /api/runs/{id}/gates.
export type GateMode = 'required' | 'advisory';
export type GateExecution = 'pending' | 'running' | 'succeeded' | 'error' | 'timeout' | 'canceled';
export type GateVerdict = 'pass' | 'fail' | 'not_applicable';
export type GateResolutionState = 'passed' | 'passed_with_warnings' | 'overridden' | 'legacy_overridden' | 'blocked' | 'error' | 'waiting' | 'incomplete';

export interface GateAttempt {
  attempt_id: string;
  gate: string;
  gate_name: string;
  mode: GateMode;
  group: number;
  execution: GateExecution;
  verdict?: GateVerdict;
  reason?: string;
  started_at: string;
  finished_at?: string;
}

export interface GateStatus {
  gate_name: string;
  gate: string;
  gate_version: number;
  mode: GateMode;
  group: number;
  attempt?: GateAttempt;
  attempts: number;
  blocking: boolean;
  warning: boolean;
  retryable: boolean;
  overridden: boolean;
}

export interface GateResolution {
  allowed: boolean;
  state: GateResolutionState;
  reasons: string[];
  blocking: string[];
  warnings: string[];
  retryable: string[];
  overridden: string[];
}

export interface GateOverride {
  stage: string;
  scope: string;
  visit?: number;
  artifact_sha256: string;
  reason: string;
  actor_id: string;
  authority: string;
  at: string;
  gates: { gate_name: string; attempt_id: string; execution: GateExecution; verdict?: GateVerdict; reason?: string }[];
}

export interface GateWave {
  stage_id: string;
  visit: number;
  stage_attempt_id: string;
  artifact_sha256: string;
  current: boolean;
  gates: GateStatus[];
  transition: GateResolution;
  override?: GateOverride;
  attempts: GateAttempt[];
}

export interface GateStage {
  stage_id: string;
  title?: string;
  groups: string[][];
  current?: GateWave;
  waves: GateWave[];
}

export interface GateWarning {
  stage_id: string;
  gate: string;
  attempt_id: string;
  execution: GateExecution;
  verdict?: GateVerdict;
  reason?: string;
}

export interface TaskGates {
  run_id: string;
  can_override: boolean;
  stages: GateStage[];
  overrides: GateOverride[];
  warnings: GateWarning[];
}
