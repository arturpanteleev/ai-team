import { cleanup, render, screen, within } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter, Routes } from '../router';
import type { WorkflowDetail, WorkflowsResponse } from '../types/workflow';
import { WorkflowDetailPage, Workflows } from './Workflows';

const { getWorkflows, getWorkflow } = vi.hoisted(() => ({
  getWorkflows: vi.fn(),
  getWorkflow: vi.fn(),
}));

vi.mock('../api', () => ({ getWorkflows, getWorkflow }));

const list: WorkflowsResponse = {
  active_workflow_id: 'idea-to-prod',
  workflows: [
    { id: 'idea-to-prod', title: 'SDLC', description: 'Standard flow', version: 3, graph_count: 2, active: true },
    { id: 'content-factory', title: 'Content', version: 2, graph_count: 1, active: false },
  ],
};

const detail: WorkflowDetail = {
  id: 'idea-to-prod', title: 'SDLC', description: 'Standard flow', version: 3, active: true,
  graphs: [
    {
      schema_version: 7, entry: 'spec', mode: 'class', graph_id: 'sdlc', title: 'SDLC graph',
      nodes: [{ id: 'spec' }, { id: 'build' }],
      edges: [{ from: 'spec', outcome: 'passed', to: 'build' }],
      returns: [{ from: 'build', to: 'spec', max_visits: 3 }],
    },
  ],
  entry_gate_groups: [[{ gate: 'verdict_marker', version: 1 }]],
  exit_gate_groups: [[{ gate: 'human_approval', version: 1, mode: 'advisory' }]],
  tasks: [{ run_id: 'run-1', feature: 'Фича', status: 'running', current_stage: 'build' }],
};

function renderAt(path: string, routes: { path: string; element: ReactNode }[]) {
  return render(<MemoryRouter initialEntries={[path]}><Routes routes={routes} /></MemoryRouter>);
}

describe('Workflow catalogue', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    getWorkflows.mockResolvedValue(list);
    getWorkflow.mockResolvedValue(detail);
  });
  afterEach(cleanup);

  it('список ведёт на карточки и показывает версию и активность', async () => {
    renderAt('/workflows', [{ path: '/workflows', element: <Workflows /> }]);

    const card = await screen.findByRole('link', { name: /SDLC/ });
    expect(card).toHaveAttribute('href', '/workflows/idea-to-prod');
    expect(card).toHaveTextContent('версия 3');
    expect(card).toHaveTextContent('графов: 2');
    expect(card).toHaveTextContent('Активный');
    expect(screen.getByRole('link', { name: /Content/ })).toHaveAttribute('href', '/workflows/content-factory');
    expect(getWorkflows).toHaveBeenCalledOnce();
  });

  it('карточка показывает графы, эффективные гейты и связанные задачи', async () => {
    renderAt('/workflows/idea-to-prod', [{ path: '/workflows/:workflowID', element: <WorkflowDetailPage /> }]);

    expect(await screen.findByRole('heading', { name: 'SDLC' })).toBeInTheDocument();
    const graphs = screen.getByRole('region', { name: 'Графы' });
    const graphLink = within(graphs).getByRole('link', { name: /SDLC graph/ });
    expect(graphLink).toHaveAttribute('href', '/graphs/sdlc');
    expect(graphLink).toHaveTextContent('вход: spec');

    expect(screen.getByRole('region', { name: 'Гейты входа (эффективные)' })).toHaveTextContent('verdict_marker@1');
    expect(screen.getByRole('region', { name: 'Гейты выхода (эффективные)' })).toHaveTextContent('human_approval@1');
    expect(screen.getByText('рекомендация')).toBeInTheDocument();

    const tasks = screen.getByRole('region', { name: 'Задачи workflow' });
    expect(within(tasks).getByRole('link', { name: 'Фича' })).toHaveAttribute('href', '/tasks/run-1');
    expect(getWorkflow).toHaveBeenCalledWith('idea-to-prod');
  });

  it('показывает понятную ошибку, когда workflow недоступен', async () => {
    getWorkflow.mockRejectedValueOnce(new Error('workflow not found'));
    renderAt('/workflows/missing', [{ path: '/workflows/:workflowID', element: <WorkflowDetailPage /> }]);

    expect(await screen.findByRole('alert')).toHaveTextContent('workflow not found');
  });
});
