import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter, Routes } from '../router';
import type { GraphView, StageStatusEntry, StageStatusKey } from '../types';
import { WorkflowGraph } from './WorkflowGraph';

const { getGraph, getRunGraph } = vi.hoisted(() => ({
  getGraph: vi.fn(),
  getRunGraph: vi.fn(),
}));

vi.mock('../api', () => ({ getGraph, getRunGraph }));

function stageEntry(stageID: string, status: StageStatusKey, current: boolean, title: string): StageStatusEntry {
  return {
    stage_id: stageID, title, index: stageID === 'spec' ? 0 : 1, status,
    readiness: status === 'not_reached' ? 'not_reached' : 'left',
    phase: 'none', outcome: '', executor: { kind: 'human', source: 'configured' },
    current, visited: status !== 'not_reached', visit_count: current ? 2 : 1, active_visit: current ? 2 : 1,
    visits: [], allowed_actions: [],
  };
}

const classView: GraphView = {
  schema_version: 7, entry: 'spec', mode: 'class', graph_id: 'sdlc',
  workflow_id: 'idea-to-prod', title: 'SDLC graph',
  nodes: [{ id: 'spec', max_visits: 3 }, { id: 'build' }],
  edges: [
    { from: 'spec', outcome: 'passed', to: 'build' },
    { from: 'build', outcome: 'passed', to: '$complete' },
  ],
  returns: [{ from: 'build', to: 'spec', max_visits: 3 }],
};

const runView: GraphView = {
  ...classView,
  mode: 'run', run_id: 'run-1', run_status: 'running', current_stage: 'build',
  stage_statuses: {
    schema_version: 7, run_status: 'running', terminal: false, current_stage: 'build',
    stages: [stageEntry('spec', 'passed', false, 'Спецификация'), stageEntry('build', 'in_progress', true, 'Сборка')],
  },
};

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes routes={[
        { path: '/graphs/:graphID', element: <WorkflowGraph /> },
        { path: '/runs/:runID/graph', element: <WorkflowGraph /> },
      ]} />
    </MemoryRouter>,
  );
}

describe('WorkflowGraph', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    getGraph.mockResolvedValue(classView);
    getRunGraph.mockResolvedValue(runView);
  });
  afterEach(cleanup);

  it('показывает узлы, подписи outcome, возвраты и легенду в режиме класса', async () => {
    renderAt('/graphs/sdlc');

    const diagram = await screen.findByRole('group', { name: 'Схема графа' });
    expect(within(diagram).getByText('spec')).toBeInTheDocument();
    expect(within(diagram).getByText('build')).toBeInTheDocument();
    expect(within(diagram).getAllByText('passed')).toHaveLength(2);
    expect(within(diagram).getByText('return_to_spec · макс. 3')).toBeInTheDocument();
    expect(within(diagram).getByText('Завершено')).toBeInTheDocument();
    expect(screen.getByText('Возврат (return_to_*)')).toBeInTheDocument();
    expect(screen.getByText('Класс')).toBeInTheDocument();

    const links = within(diagram).getAllByRole('link', { name: /Открыть workflow/ });
    expect(links).toHaveLength(2);
    expect(links[0]).toHaveAttribute('href', '/workflows/idea-to-prod');
    expect(getGraph).toHaveBeenCalledWith('sdlc');
    expect(getRunGraph).not.toHaveBeenCalled();
  });

  it('в режиме прогона подсвечивает текущий этап и его исходящие рёбра', async () => {
    renderAt('/runs/run-1/graph');

    const diagram = await screen.findByRole('group', { name: 'Схема графа' });
    const links = within(diagram).getAllByRole('link', { name: /Открыть задачу/ });
    expect(links[0]).toHaveAttribute('href', '/tasks/run-1');
    expect(within(diagram).getByRole('link', { name: 'Открыть задачу: Сборка' })).toHaveAttribute('aria-current', 'step');
    expect(within(diagram).getByText('Сборка').closest('g')).toHaveAttribute('data-current', 'true');
    expect(diagram.querySelectorAll('[data-active="true"]').length).toBeGreaterThan(0);
    expect(screen.getByText('Прогон')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: '← К задаче' })).toHaveAttribute('href', '/tasks/run-1');
    expect(getRunGraph).toHaveBeenCalledWith('run-1');
    expect(getGraph).not.toHaveBeenCalled();
  });

  it('сообщает об ошибке загрузки графа', async () => {
    getGraph.mockRejectedValueOnce(new Error('граф не найден'));
    renderAt('/graphs/missing');

    expect(await screen.findByRole('alert')).toHaveTextContent('граф не найден');
  });
});
