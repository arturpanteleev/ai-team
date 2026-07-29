import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from '../router';
import { PipelineDetail } from './PipelineDetail';

vi.mock('../hooks/useWebSocket', () => ({
  useWebSocket: () => ({ connected: true }),
}));

vi.mock('../api', () => ({
  getActivePrincipal: () => null,
  getPipelineRun: vi.fn().mockResolvedValue({
    run: {
      id: 7, run_id: 'run-graph', feature: 'graph-feature',
      status: 'waiting_for_approval', started_at: '2026-07-28T00:00:00Z',
    },
    stages: [],
    approvals: [],
    next_stage: 'architect',
  }),
  getPipelineArtifacts: vi.fn().mockResolvedValue([]),
  getRunWorkflow: vi.fn().mockResolvedValue({
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
  }),
  decideApproval: vi.fn(),
  resumeRun: vi.fn(),
  cancelRun: vi.fn(),
}));

describe('PipelineDetail graph', () => {
  it('показывает immutable graph, policy и текущий узел', async () => {
    render(
      <MemoryRouter initialEntries={['/pipelines/7']}>
        <PipelineDetail />
      </MemoryRouter>,
    );

    expect(await screen.findByText('Маршрут workflow')).toBeInTheDocument();
    expect(screen.getByText('architect · max 2')).toHaveAttribute('data-current', 'true');
    expect(screen.getByText(/product_owner · quorum any/)).toHaveTextContent('approve→architect');
  });
});
