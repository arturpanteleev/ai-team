import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter, Routes } from '../router';
import type { TaskRunDetails } from '../types';
import { TaskDetail } from './TaskDetail';

vi.mock('../hooks/useWebSocket', () => ({ useWebSocket: () => undefined }));

vi.mock('../api', () => ({
  answerTaskGrill: vi.fn(), approveTaskDeliveryPlan: vi.fn(), cancelRun: vi.fn(), confirmTaskStage: vi.fn(), decideApproval: vi.fn(),
  finishTaskGrill: vi.fn(), getActivePrincipal: vi.fn().mockReturnValue(null), getArtifact: vi.fn(),
  getPipelineArtifacts: vi.fn().mockResolvedValue([]),
  getPipelineRun: vi.fn().mockResolvedValue({ run: { id: 7, run_id: 'run-loop' }, stages: [] }),
  resumeRun: vi.fn(), retryDelivery: vi.fn(), getRunLog: vi.fn().mockResolvedValue({ content: '', truncated: false }),
  getTaskGates: vi.fn(), getTaskRunDetails: vi.fn(), retryTaskGate: vi.fn(),
  getTaskRunEvents: vi.fn().mockResolvedValue({ events: [], next_cursor: 0 }),
  overrideTaskStageCheck: vi.fn(), returnTaskStage: vi.fn(), runTaskStageAgent: vi.fn(), setTaskStageExecutor: vi.fn(),
  skipTaskStage: vi.fn(), submitTaskGrillVersion: vi.fn(), submitTaskStageResult: vi.fn(), takeTaskStage: vi.fn(),
  isAuthenticationRequired: () => false,
  getTaskFlowMetrics: vi.fn().mockResolvedValue({ by_stage: [] }),
  getRunTracker: vi.fn().mockResolvedValue({ configured: false, sync_status: 'not_configured' }),
  getTrackerOverview: vi.fn().mockResolvedValue({ configured: false }),
  setRunTrackerLink: vi.fn(), removeRunTrackerLink: vi.fn(), retryRunTrackerDeliveries: vi.fn(), dismissTrackerInbound: vi.fn(), syncTrackerNow: vi.fn(),
}));

const startedAt = '2026-10-12T09:00:00Z';
const reviewPath = '/target/.ai-team/artifacts/feat/review.md';
const reviewMarkdown = '## Замечания ревьюера\n\nЗамени обработку пустого списка на явную проверку.\n\n**Verdict:** CHANGES_REQUESTED';

const reviewInput = JSON.stringify([{ Name: 'review', Path: reviewPath, Size: 128, ModTime: startedAt }]);

const loopDetails: TaskRunDetails = {
  run: { id: 7, run_id: 'run-loop', feature: 'Цикл код-ревью', status: 'running', started_at: startedAt },
  task: 'Проверить внутренний цикл ревью',
  mode: 'auto',
  template: {
    valid: true, template: 'review-loop-internal', title: 'Цикл код-ревью (внутренний агент)', version: 'a'.repeat(64),
    stages: [
      { id: 'implementation', title: 'Техническая реализация', function: 'developer', result: 'md', executor: 'agent', agent: 'demo-coder' },
      { id: 'code_review', title: 'Код-ревью', function: 'reviewer', result: 'md', executor: 'agent', agent: 'demo-reviewer' },
    ],
    returns: [{ from: 'code_review', to: 'implementation' }],
  },
  state: { run_id: 'run-loop', task: 'Проверить внутренний цикл ревью', phase: 'running', next_stage: 'implementation', attempt_ordinal: 3 },
  stages: [
    {
      id: 1, pipeline_run_id: 7, attempt_id: 'code-review-attempt-1', stage_index: 2, agent_name: 'code_review',
      status: 'passed', started_at: startedAt, completed_at: '2026-10-12T09:10:00Z',
      outputs_json: reviewInput,
    },
    {
      id: 2, pipeline_run_id: 7, attempt_id: 'implementation-attempt-2', stage_index: 3, agent_name: 'implementation',
      status: 'running', started_at: '2026-10-12T09:11:00Z', inputs_json: reviewInput,
    },
  ],
  approvals: [],
  stage_owners: {},
  results: {},
  allowed_actions: [],
  delivery: { status: 'not_requested' },
  stage_statuses: {
    schema_version: 1, terminal: false, current_stage: 'implementation',
    stages: [
      {
        stage_id: 'implementation', index: 1, status: 'in_progress', readiness: 'engaged', phase: 'working', outcome: 'pending',
        executor: { kind: 'internal_agent', agent: 'demo-coder', source: 'configured' },
        current: true, visited: true, visit_count: 2, active_visit: 2, allowed_actions: [],
        visits: [
          { number: 1, active: false, entered_at: startedAt, entered_via: 'start', outcome: 'passed', executor: { kind: 'internal_agent', agent: 'demo-coder', source: 'configured' }, attempts: [] },
          { number: 2, active: true, entered_at: '2026-10-12T09:11:00Z', entered_from: 'code_review', entered_via: 'return', outcome: 'pending', executor: { kind: 'internal_agent', agent: 'demo-coder', source: 'configured' }, attempts: [] },
        ],
      },
      {
        stage_id: 'code_review', index: 2, status: 'passed', readiness: 'left', phase: 'none', outcome: 'passed',
        executor: { kind: 'internal_agent', agent: 'demo-reviewer', source: 'configured' },
        current: false, visited: true, visit_count: 1, active_visit: 1, allowed_actions: [],
        visits: [{ number: 1, active: false, entered_at: startedAt, entered_via: 'transition', outcome: 'passed', executor: { kind: 'internal_agent', agent: 'demo-reviewer', source: 'configured' }, attempts: [] }],
      },
    ],
  },
};

const failedWave = {
  stage_id: 'code_review', visit: 1, stage_attempt_id: 'code-review-attempt-1', artifact_sha256: 'a'.repeat(64), current: true,
  gates: [{ gate_name: 'verdict_marker', gate: 'verdict_marker', gate_version: 1, mode: 'required' as const, group: 0, attempts: 1,
    blocking: true, warning: false, retryable: false, overridden: false,
    attempt: { attempt_id: 'gate-verdict-marker-1', gate: 'verdict_marker', gate_name: 'verdict_marker', mode: 'required' as const, group: 0,
      execution: 'succeeded' as const, verdict: 'fail' as const, reason: 'Verdict: CHANGES_REQUESTED', started_at: startedAt, finished_at: '2026-10-12T09:10:00Z' } }],
  transition: { allowed: false, state: 'blocked' as const, reasons: ['verdict_marker: провал'], blocking: ['verdict_marker'], warnings: [], retryable: [], overridden: [] },
  attempts: [],
};

const failedGateGates = {
  run_id: 'run-loop', can_override: false, overrides: [], warnings: [],
  stages: [{
    stage_id: 'code_review', title: 'Код-ревью', groups: [['verdict_marker']],
    waves: [failedWave], current: failedWave,
  }],
};

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={['/tasks/run-loop']}>
      <Routes routes={[{ path: '/tasks/:runID', element: <TaskDetail /> }]} />
    </MemoryRouter>,
  );
}

beforeEach(async () => {
  const api = await import('../api');
  vi.mocked(api.getTaskRunDetails).mockReset().mockResolvedValue(loopDetails);
  vi.mocked(api.getTaskGates).mockReset().mockResolvedValue(failedGateGates as never);
  vi.mocked(api.getPipelineArtifacts).mockReset().mockResolvedValue([]);
  vi.mocked(api.getArtifact).mockReset().mockImplementation(async (_runId: string, path: string) =>
    path.endsWith('review.md') ? reviewMarkdown : '');
  vi.mocked(api.getTaskRunEvents).mockReset().mockResolvedValue({ events: [], next_cursor: 0 });
});

afterEach(cleanup);

describe('loop-internal: review feedback on the implementation stage', () => {
  it('shows the review artifact content inline without opening the artifact', async () => {
    renderDetail();

    expect(await screen.findByText('Что исправить')).toBeInTheDocument();
    expect(screen.getByText(/Замени обработку пустого списка/)).toBeInTheDocument();
    expect(screen.getByText(/CHANGES_REQUESTED/)).toBeInTheDocument();
    const api = await import('../api');
    expect(api.getArtifact).toHaveBeenCalledWith('run-loop', expect.stringMatching(/review\.md$/));
  });

  it('links the failed gate that caused the return', async () => {
    renderDetail();

    expect(await screen.findByText(/verdict_marker/)).toBeInTheDocument();
    const attention = screen.getByRole('region', { name: 'Требует внимания' });
    expect(within(attention).getByRole('button', { name: /гейт/i })).toBeInTheDocument();
  });

  it('does not show the feedback block when the stage has no review input', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({
      ...loopDetails,
      stages: loopDetails.stages.map((stage) => ({ ...stage, inputs_json: undefined })),
    });
    renderDetail();

    expect(await screen.findByRole('heading', { name: 'Цикл код-ревью', level: 1 })).toBeInTheDocument();
    expect(screen.queryByText('Что исправить')).not.toBeInTheDocument();
  });
});
