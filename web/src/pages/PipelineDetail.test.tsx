import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from '../router';
import { PipelineDetail } from './PipelineDetail';

vi.mock('../hooks/useWebSocket', () => ({
  useWebSocket: () => ({ connected: true }),
}));

const api = vi.hoisted(() => ({
  getActivePrincipal: vi.fn(),
  getPipelineRun: vi.fn(),
  getPipelineArtifacts: vi.fn(),
  getRunWorkflow: vi.fn(),
  decideApproval: vi.fn(),
  resumeRun: vi.fn(),
  cancelRun: vi.fn(),
}));

vi.mock('../api', () => api);

const workflow = {
  schema_version: 2,
  graph: {
    schema_version: 4,
    entry: 'analyst',
    nodes: [{ name: 'analyst' }, { name: 'architect', max_visits: 2 }],
    edges: [{
      from: 'analyst', outcome: 'passed', to: 'architect',
      approval: {
        roles: ['product_owner'], quorum: 'any',
        actions: { approve: 'architect', reject: '$stop' },
      },
    }],
  },
};

const pendingApproval = {
  id: 'approval-1',
  run_id: 'run-graph',
  attempt_id: 'attempt-1',
  from_stage: 'analyst',
  to_stage: 'architect',
  trigger: 'stage_completed',
  subject_hash: 'a'.repeat(64),
  required_roles: ['product_owner'],
  quorum: 'any',
  actions: ['approve', 'reject'],
  status: 'pending',
  created_at: '2026-07-28T00:00:00Z',
};

function mockRun(approvals: unknown[]) {
  api.getPipelineRun.mockResolvedValue({
    run: {
      id: 7, run_id: 'run-graph', feature: 'graph-feature',
      status: 'waiting_for_approval', started_at: '2026-07-28T00:00:00Z',
    },
    stages: [],
    approvals,
    next_stage: 'architect',
  });
}

beforeEach(() => {
  // globals в vitest не включены, поэтому auto-cleanup RTL не зарегистрирован:
  // без явного cleanup предыдущий DOM остаётся и queryBy* находит чужие узлы.
  cleanup();
  vi.clearAllMocks();
  api.getActivePrincipal.mockReturnValue(null);
  api.getPipelineArtifacts.mockResolvedValue([]);
  api.getRunWorkflow.mockResolvedValue(workflow);
  api.decideApproval.mockResolvedValue({ ...pendingApproval, status: 'resolved' });
  mockRun([]);
});

function renderDetail() {
  render(
    <MemoryRouter initialEntries={['/pipelines/7']}>
      <PipelineDetail />
    </MemoryRouter>,
  );
}

describe('PipelineDetail graph', () => {
  it('показывает immutable graph, policy и текущий узел', async () => {
    renderDetail();

    expect(await screen.findByText('Маршрут workflow')).toBeInTheDocument();
    expect(screen.getByText('architect · max 2')).toHaveAttribute('data-current', 'true');
    expect(screen.getByText(/product_owner · quorum any/)).toHaveTextContent('approve→architect');
  });
});

describe('PipelineDetail решения', () => {
  it('отправляет только action и exact subject, без actor и роли', async () => {
    api.getActivePrincipal.mockReturnValue({ actor_id: 'po-1', roles: ['product_owner'] });
    mockRun([pendingApproval]);
    renderDetail();

    fireEvent.click(await screen.findByRole('button', { name: /approve · product_owner/ }));

    await waitFor(() => expect(api.decideApproval).toHaveBeenCalledTimes(1));
    expect(api.decideApproval).toHaveBeenCalledWith('run-graph', pendingApproval, { action: 'approve' });
    // Actor показывается из trusted principal, а не вводится человеком.
    expect(screen.queryByLabelText('Actor identity')).toBeNull();
    expect(screen.getByText('po-1')).toBeInTheDocument();
  });

  it('не предлагает решение, если у principal нет требуемой роли', async () => {
    api.getActivePrincipal.mockReturnValue({ actor_id: 'qa-1', roles: ['qa'] });
    mockRun([pendingApproval]);
    renderDetail();

    expect(await screen.findByText(/Нет роли для этого решения/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /approve/ })).toBeNull();
  });
});
