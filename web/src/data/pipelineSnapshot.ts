import {
  getPipelineArtifacts,
  getPipelineRun,
  getRunWorkflow,
} from '../api';
import type {
  Approval,
  Artifact,
  DeliveryProjection,
  PipelineRun,
  Stage,
  WorkflowGraph,
  WsEvent,
} from '../types';

export interface PipelineSnapshot {
  run: PipelineRun;
  stages: Stage[];
  artifacts: Artifact[];
  approvals: Approval[];
  delivery: DeliveryProjection;
  graph: WorkflowGraph | null;
  nextStage: string;
}

export async function loadPipelineSnapshot(id: number): Promise<PipelineSnapshot> {
  const pipelineData = await getPipelineRun(id);
  const [artifacts, workflow] = await Promise.all([
    getPipelineArtifacts(id),
    getRunWorkflow(pipelineData.run.run_id).catch(() => null),
  ]);
  return {
    run: pipelineData.run,
    stages: pipelineData.stages,
    artifacts,
    approvals: pipelineData.approvals ?? [],
    delivery: pipelineData.delivery ?? { status: 'not_requested' },
    graph: workflow?.graph ?? null,
    nextStage: pipelineData.next_stage ?? '',
  };
}

export async function refreshPipelineSnapshot(
  current: PipelineSnapshot,
  event: WsEvent,
): Promise<PipelineSnapshot> {
  if (event.run_id !== current.run.run_id) return current;

  const [pipelineData, artifacts] = await Promise.all([
    getPipelineRun(current.run.id),
    event.type === 'attempt_finished' || event.type === 'attempts_invalidated'
      ? getPipelineArtifacts(current.run.id)
      : Promise.resolve(current.artifacts),
  ]);

  return {
    ...current,
    run: pipelineData.run,
    stages: pipelineData.stages,
    approvals: pipelineData.approvals ?? [],
    delivery: pipelineData.delivery ?? { status: 'not_requested' },
    nextStage: pipelineData.next_stage ?? '',
    artifacts,
  };
}
