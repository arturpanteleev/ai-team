import { beforeEach, describe, expect, it, vi } from 'vitest';
import { getPipelineArtifacts, getPipelineRun } from '../api';
import type { PipelineSnapshot } from './pipelineSnapshot';
import { refreshPipelineSnapshot } from './pipelineSnapshot';

vi.mock('../api', () => ({
  getPipelineArtifacts: vi.fn(),
  getPipelineRun: vi.fn(),
}));

const snapshot: PipelineSnapshot = {
  run: {
    id: 7,
    run_id: 'run-7',
    feature: 'Проверка обновления',
    status: 'running',
    started_at: '2026-10-09T00:00:00Z',
  },
  stages: [],
  artifacts: [],
  approvals: [],
  delivery: { status: 'not_requested' },
  graph: { schema_version: 1, entry: 'idea', nodes: [], edges: [] },
  nextStage: 'idea',
  stageOwners: {},
};

const event = (type: 'approval_requested' | 'attempt_finished') => ({
  version: 1 as const,
  cursor: 1,
  run_id: 'run-7',
  sequence: 1,
  type,
  timestamp: '2026-10-09T00:00:01Z',
  data: {},
});

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(getPipelineRun).mockResolvedValue({
    run: { ...snapshot.run, status: 'waiting_for_approval' },
    stages: [],
    approvals: [],
    delivery: { status: 'pending' },
  });
  vi.mocked(getPipelineArtifacts).mockResolvedValue([{ name: 'result.md', path: 'result.md', run_id: 'run-7', size: 12, mod_time: '' }]);
});

describe('pipeline snapshot event refresh', () => {
  it('refreshes task state on approval events without reloading artifacts or workflow', async () => {
    const updated = await refreshPipelineSnapshot(snapshot, event('approval_requested'));

    expect(updated.run.status).toBe('waiting_for_approval');
    expect(updated.delivery.status).toBe('pending');
    expect(updated.graph).toBe(snapshot.graph);
    expect(updated.artifacts).toBe(snapshot.artifacts);
    expect(getPipelineRun).toHaveBeenCalledWith(7);
    expect(getPipelineArtifacts).not.toHaveBeenCalled();
  });

  it('refreshes artifact data after an attempt finishes', async () => {
    const updated = await refreshPipelineSnapshot(snapshot, event('attempt_finished'));

    expect(updated.artifacts).toHaveLength(1);
    expect(getPipelineArtifacts).toHaveBeenCalledWith(7);
  });

  it('ignores events for another run without making API calls', async () => {
    const updated = await refreshPipelineSnapshot(snapshot, { ...event('approval_requested'), run_id: 'other-run' });

    expect(updated).toBe(snapshot);
    expect(getPipelineRun).not.toHaveBeenCalled();
    expect(getPipelineArtifacts).not.toHaveBeenCalled();
  });
});
