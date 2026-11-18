import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter, Routes } from '../router';
import type { WorkflowDetail, WorkflowsResponse } from '../types/workflow';
import { WorkflowDetailPage, Workflows } from './Workflows';

const { getWorkflows, getWorkflow, getActivePrincipal, updateWorkflowProcess } = vi.hoisted(() => ({
  getWorkflows: vi.fn(),
  getWorkflow: vi.fn(),
  getActivePrincipal: vi.fn(),
  updateWorkflowProcess: vi.fn(),
}));

vi.mock('../api', () => ({ getWorkflows, getWorkflow, getActivePrincipal, updateWorkflowProcess }));

const editor = () => screen.getByRole('region', { name: 'Настройки workflow' });

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
    getActivePrincipal.mockReturnValue({ actor_id: 'po@example.com', roles: ['product_owner'] });
    updateWorkflowProcess.mockResolvedValue({ version: 'a'.repeat(64), workflow_id: 'idea-to-prod' });
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

  it('сохраняет процессные поля и обновляет карточку', async () => {
    renderAt('/workflows/idea-to-prod', [{ path: '/workflows/:workflowID', element: <WorkflowDetailPage /> }]);

    const titleInput = await within(editor()).findByLabelText('Название');
    const descriptionInput = within(editor()).getByLabelText('Описание');
    fireEvent.change(titleInput, { target: { value: 'SDLC v2' } });
    fireEvent.change(descriptionInput, { target: { value: 'Новое описание' } });
    await waitFor(() => expect(titleInput).toHaveValue('SDLC v2'));
    fireEvent.click(within(editor()).getByRole('button', { name: 'Сохранить' }));

    await waitFor(() => expect(updateWorkflowProcess).toHaveBeenCalledWith('idea-to-prod', {
      title: 'SDLC v2', description: 'Новое описание',
    }));
    expect(await screen.findByRole('status')).toHaveTextContent('сохранено');
    expect(getWorkflow).toHaveBeenCalledTimes(2);
    // Gate chains stay read-only: no gate controls are rendered anywhere.
    expect(within(editor()).queryByLabelText(/гейт/i)).toBeNull();
  });

  it('без права template:edit не предлагает правку процессных полей', async () => {
    getActivePrincipal.mockReturnValue({ actor_id: 'dev@example.com', roles: ['developer'] });
    renderAt('/workflows/idea-to-prod', [{ path: '/workflows/:workflowID', element: <WorkflowDetailPage /> }]);

    expect(await screen.findByLabelText('Название')).toBeDisabled();
    expect(screen.getByLabelText('Описание')).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Сохранить' })).toBeNull();
    expect(updateWorkflowProcess).not.toHaveBeenCalled();
  });

  it('показывает реальный отказ сервера с кодом trusted_config_only', async () => {
    // F3: mock the error the server actually sends: the long explanation in
    // `error` and the machine code in `code`, joined by the API client.
    const serverError = 'шаблон, присланный через веб (REST, MCP или страницу «Шаблон»), может менять только процесс ' +
      'и только ужесточать forbid_self_approval, returns и max_visits; команды, образы, mounts и ' +
      'инфраструктурные настройки должны совпадать с активным файловым конфигом: ' +
      'workflows: гейт verdict_marker (verdict_marker@1) файлового конфига нельзя убрать или ослабить из веба';
    updateWorkflowProcess.mockRejectedValueOnce(new Error(`trusted_config_only: ${serverError}`));
    renderAt('/workflows/idea-to-prod', [{ path: '/workflows/:workflowID', element: <WorkflowDetailPage /> }]);

    fireEvent.change(await screen.findByLabelText('Описание'), { target: { value: 'oslabit' } });
    fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }));

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('trusted_config_only:');
    expect(alert).toHaveTextContent('гейт verdict_marker (verdict_marker@1) файлового конфига нельзя убрать или ослабить из веба');
    expect(screen.queryByRole('status')).toBeNull();
  });
});
