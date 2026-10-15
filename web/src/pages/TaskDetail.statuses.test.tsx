import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter, Routes } from '../router';
import type { TaskRunDetails } from '../types';
import { TaskDetail } from './TaskDetail';

vi.mock('../hooks/useWebSocket', () => ({ useWebSocket: () => undefined }));

vi.mock('../api', () => ({
  answerTaskGrill: vi.fn(), approveTaskDeliveryPlan: vi.fn(), cancelRun: vi.fn(), confirmTaskStage: vi.fn(), decideApproval: vi.fn(),
  finishTaskGrill: vi.fn(), getActivePrincipal: vi.fn().mockReturnValue(null), getArtifact: vi.fn().mockResolvedValue(''),
  getPipelineArtifacts: vi.fn().mockResolvedValue([]),
  getPipelineRun: vi.fn().mockResolvedValue({ run: { id: 5, run_id: 'run-detail-1' }, stages: [] }),
  resumeRun: vi.fn(), retryDelivery: vi.fn(), getRunLog: vi.fn().mockResolvedValue({ content: '', truncated: false }),
  getTaskGates: vi.fn().mockResolvedValue({ run_id: 'run-detail-1', can_override: false, stages: [], overrides: [], warnings: [] }),
  getTaskRunDetails: vi.fn(), retryTaskGate: vi.fn(),
  getTaskRunEvents: vi.fn().mockResolvedValue({ events: [], next_cursor: 0 }),
  getTaskFlowMetrics: vi.fn().mockResolvedValue({ by_stage: [] }),
  overrideTaskStageCheck: vi.fn(), returnTaskStage: vi.fn(), runTaskStageAgent: vi.fn(), setTaskStageExecutor: vi.fn(),
  skipTaskStage: vi.fn(), submitTaskGrillVersion: vi.fn(), submitTaskStageResult: vi.fn(), takeTaskStage: vi.fn(),
  isAuthenticationRequired: () => false,
  getRunTracker: vi.fn().mockResolvedValue({ configured: false, sync_status: 'not_configured' }),
  getTrackerOverview: vi.fn().mockResolvedValue({ configured: false }),
  setRunTrackerLink: vi.fn(), removeRunTrackerLink: vi.fn(), retryRunTrackerDeliveries: vi.fn(), dismissTrackerInbound: vi.fn(), syncTrackerNow: vi.fn(),
}));

const baseDetails: TaskRunDetails = {
  run: { id: 5, run_id: 'run-detail-1', feature: 'Новый маршрут', status: 'running', started_at: '2026-10-12T09:00:00Z' },
  task: 'Сократить путь до первой ценности',
  mode: 'manual',
  template: {
    valid: true, template: 'pinned-route', title: 'Закреплённый маршрут', version: 'a'.repeat(64),
    stages: [
      { id: 'intent', title: 'Идея', function: 'product_owner', result: 'md', executor: 'human' },
      { id: 'analysis', title: 'Анализ', function: 'analyst', result: 'md', executor: 'agent', agent: 'analyst' },
      { id: 'release', title: 'Релиз', function: 'release_manager', result: 'approve', executor: 'human', skippable: true },
    ],
    returns: [{ from: 'release', to: 'intent' }],
  },
  state: { run_id: 'run-detail-1', task: 'Сократить путь до первой ценности', phase: 'running', next_stage: 'analysis', attempt_ordinal: 2 },
  stages: [],
  approvals: [],
  stage_owners: {},
  results: {},
  allowed_actions: [],
  delivery: { status: 'not_requested' },
  stage_statuses: {
    schema_version: 1, terminal: false, current_stage: 'analysis',
    stages: [
      {
        stage_id: 'intent', index: 1, status: 'passed', readiness: 'left', phase: 'none', outcome: 'passed',
        executor: { kind: 'human', actor: 'alice', source: 'attempt' }, current: false, visited: true, visit_count: 1, active_visit: 0,
        allowed_actions: [], visits: [{ number: 1, active: false, entered_at: '2026-10-12T09:00:00Z', entered_via: 'start', left_at: '2026-10-12T09:10:00Z',
          left_to: 'analysis', outcome: 'passed', executor: { kind: 'human', actor: 'alice', source: 'attempt' }, attempts: [] }],
      },
      {
        stage_id: 'analysis', index: 2, status: 'in_progress', readiness: 'engaged', phase: 'working', outcome: 'pending',
        executor: { kind: 'internal_agent', agent: 'analyst', name: 'agentcli', source: 'configured' }, current: true, visited: true,
        visit_count: 2, active_visit: 2, allowed_actions: ['run-agent'],
        visits: [
          { number: 1, active: false, entered_at: '2026-10-12T09:10:00Z', entered_from: 'intent', entered_via: 'transition', left_at: '2026-10-12T09:20:00Z',
            left_to: 'release', outcome: 'passed', executor: { kind: 'internal_agent', agent: 'analyst', source: 'attempt' }, attempts: [] },
          { number: 2, active: true, entered_at: '2026-10-12T09:30:00Z', entered_from: 'release', entered_via: 'return', outcome: 'pending',
            executor: { kind: 'internal_agent', agent: 'analyst', source: 'configured' }, attempts: [] },
        ],
      },
      {
        stage_id: 'release', index: 3, status: 'not_reached', readiness: 'not_reached', phase: 'none', outcome: 'rejected',
        executor: { kind: 'human', source: 'attempt' }, current: false, visited: true, visit_count: 1, active_visit: 0, allowed_actions: [],
        visits: [{ number: 1, active: false, entered_at: '2026-10-12T09:20:00Z', entered_from: 'analysis', entered_via: 'transition',
          left_at: '2026-10-12T09:30:00Z', left_to: 'analysis', returned: true, superseded: true, outcome: 'rejected',
          executor: { kind: 'human', actor: 'bob', source: 'attempt' }, attempts: [] }],
      },
    ],
  },
};

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={['/tasks/run-detail-1']}>
      <Routes routes={[{ path: '/tasks/:runID', element: <TaskDetail /> }]} />
    </MemoryRouter>,
  );
}

beforeEach(async () => {
  const api = await import('../api');
  vi.mocked(api.getTaskRunDetails).mockReset().mockResolvedValue(baseDetails);
});

afterEach(cleanup);

describe('Task detail stage statuses (S11)', () => {
  it('shows the computed status, the executor and the visits in the route and the current stage', async () => {
    renderDetail();
    const route = await screen.findByRole('region', { name: 'Маршрут процесса' });
    expect(within(route).getByText('Пройден')).toBeInTheDocument();
    expect(within(route).getByText('В работе')).toBeInTheDocument();
    expect(within(route).getByText('Не достигнут')).toBeInTheDocument();
    expect(within(route).getByText('Человек: alice')).toBeInTheDocument();
    expect(within(route).getByText('Внутренний агент: analyst (agentcli)')).toBeInTheDocument();
    expect(within(route).getByText('Посещений: 2, сейчас 2-е')).toBeInTheDocument();
    expect(within(route).getByText('Бывал активен ранее')).toBeInTheDocument();
    expect(within(route).getByText('Доступно: Поручить агенту')).toBeInTheDocument();
    const status = screen.getByRole('region', { name: 'Анализ' });
    expect(within(status).getByText('В работе')).toBeInTheDocument();
    expect(within(status).getByText('Внутренний агент: analyst (agentcli)')).toBeInTheDocument();
    expect(within(status).getByText('Посещение 2 из 2 (возврат)')).toBeInTheDocument();
    fireEvent.click(screen.getByText('Посещения этапа (2)'));
    expect(screen.getByText(/Посещение 1 · неактивно · пройден/)).toBeInTheDocument();
    expect(screen.getByText(/Посещение 2 · активно · не завершён · Внутренний агент: analyst · возврат с «Релиз»/)).toBeInTheDocument();
  });

  it('says which stage waits when a confirmation opens the next one', async () => {
    const api = await import('../api');
    const waiting: TaskRunDetails = {
      ...baseDetails,
      state: { ...baseDetails.state!, phase: 'waiting', next_stage: 'release', pending_approval_id: 'c1' },
      stage_statuses: {
        ...baseDetails.stage_statuses!, current_stage: 'analysis',
        stages: baseDetails.stage_statuses!.stages.map((entry) => entry.stage_id === 'analysis'
          ? { ...entry, status: 'awaiting_human' as const, phase: 'awaiting_human' as const, outcome: 'passed',
            blocked_by: { kind: 'approval', roles: ['product_owner'], actions: ['approve'] }, action_stage_id: 'release' }
          : entry),
      },
    };
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(waiting);
    renderDetail();
    expect(await screen.findByText(/Сейчас ждёт этап «Анализ»; после решения откроется «Релиз»/)).toBeInTheDocument();
    expect(screen.getByText(/нужно подтверждение результата \(роли: product_owner\)/)).toBeInTheDocument();
  });

  it('shows the gates of the stage whose transition awaits confirmation, not of the next one (S17)', async () => {
    const api = await import('../api');
    const waiting: TaskRunDetails = {
      ...baseDetails,
      state: { ...baseDetails.state!, phase: 'waiting', next_stage: 'release', pending_approval_id: 'c1' },
      stage_statuses: {
        ...baseDetails.stage_statuses!, current_stage: 'analysis',
        stages: baseDetails.stage_statuses!.stages.map((entry) => entry.stage_id === 'analysis'
          ? { ...entry, status: 'awaiting_human' as const, phase: 'awaiting_human' as const, outcome: 'passed',
            blocked_by: { kind: 'approval', roles: ['product_owner'], actions: ['approve'] }, action_stage_id: 'release' }
          : entry),
      },
    };
    const wave = {
      stage_id: 'analysis', visit: 1, stage_attempt_id: 'a1', artifact_sha256: 'b'.repeat(64), current: true,
      gates: [{ gate_name: 'required_sections', gate: 'required_sections', gate_version: 1, mode: 'required' as const, group: 0, attempts: 1,
        blocking: false, warning: false, retryable: false, overridden: false,
        attempt: { attempt_id: 'g1', execution: 'succeeded', verdict: 'pass', started_at: '2026-10-12T09:00:00Z' } }],
      transition: { allowed: true, state: 'allowed', reasons: [], blocking: [], warnings: [], retryable: [], overridden: [] }, attempts: [],
    };
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(waiting);
    vi.mocked(api.getTaskGates).mockResolvedValue({
      run_id: 'run-detail-1', can_override: false, overrides: [], warnings: [],
      stages: [{ stage_id: 'analysis', groups: [['required_sections']], current: wave, waves: [wave] }],
    } as never);
    renderDetail();
    const panel = await screen.findByTestId('gate-panel');
    expect(within(panel).getByText('required_sections')).toBeInTheDocument();
  });

  it('keeps the old route when the server sent no projection', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({ ...baseDetails, stage_statuses: undefined });
    renderDetail();
    const route = await screen.findByRole('region', { name: 'Маршрут процесса' });
    expect(within(route).getByText('текущий')).toBeInTheDocument();
    expect(within(route).queryByText('Не достигнут')).not.toBeInTheDocument();
  });
});
