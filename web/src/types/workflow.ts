// Типы каталога workflow: GET /api/workflows и GET /api/workflows/{workflowID}.
// Workflow — это класс (набор графов), прогон — объект (одна задача). Оба
// режима видны в данных: карточка несёт графы, а связь с задачами — список
// прогонов, закреплённых за этим workflow.

import type { GraphView } from './index';

export interface WorkflowListItem {
  id: string;
  title: string;
  description?: string;
  /** Metadata version declared in the reviewed configuration. */
  version: number;
  graph_count: number;
  active: boolean;
}

export interface WorkflowsResponse {
  workflows: WorkflowListItem[];
  active_workflow_id: string;
}

/** One gate reference of an effective chain (version is never implicit). */
export interface WorkflowGateRef {
  gate: string;
  version: number;
  /** Instance name, used to override one inherited reference. */
  name?: string;
  params?: Record<string, unknown>;
  mode?: 'required' | 'advisory';
}

/** Groups run sequentially; gates inside one group run in parallel. */
export type WorkflowGateGroups = WorkflowGateRef[][];

export interface WorkflowTaskRef {
  run_id: string;
  feature: string;
  status?: string;
  current_stage?: string;
}

export interface WorkflowDetail {
  id: string;
  title: string;
  description?: string;
  version: number;
  active: boolean;
  /** Class-mode graphs; the task page shows the same graph in run mode. */
  graphs: GraphView[];
  /** Fully resolved chains: inheritance, point overrides and version checks. */
  entry_gate_groups: WorkflowGateGroups;
  exit_gate_groups: WorkflowGateGroups;
  /** Runs whose immutable pin selects this workflow. */
  tasks: WorkflowTaskRef[];
}
