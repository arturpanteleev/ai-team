import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter, Routes } from '../router';
import type { GraphView, StageStatusEntry, StageStatusKey } from '../types';
import { buildLayout, WorkflowGraph } from './WorkflowGraph';
import styles from './WorkflowGraph.module.css';

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

// Снимок реальной конфигурации дефолтного шаблона:
// config.DefaultProfile(config.ProfileStandard).CompiledGraph() + cfg.Returns.
// У tech_design прямые passed/skipped, обратный rejected и возврат return_to_product_spec,
// у code_review/qa/observation — возвраты return_to_implementation.
const defaultTemplateView: GraphView = {
  schema_version: 7, entry: 'intent', mode: 'class', graph_id: 'sdlc-default', workflow_id: 'idea-to-prod',
  title: 'SDLC default template',
  nodes: [
    { id: 'intent' }, { id: 'product_spec', max_visits: 3 }, { id: 'tech_design' },
    { id: 'implementation', max_visits: 3 }, { id: 'code_review' }, { id: 'qa' },
    { id: 'deploy' }, { id: 'observation' }, { id: 'acceptance' },
  ],
  edges: [
    { from: 'intent', outcome: 'passed', to: 'product_spec' },
    { from: 'product_spec', outcome: 'passed', to: 'tech_design' },
    { from: 'tech_design', outcome: 'skipped', to: 'implementation' },
    { from: 'tech_design', outcome: 'passed', to: 'implementation' },
    { from: 'implementation', outcome: 'passed', to: 'code_review' },
    { from: 'code_review', outcome: 'passed', to: 'qa' },
    { from: 'qa', outcome: 'passed', to: 'deploy' },
    { from: 'deploy', outcome: 'passed', to: 'observation' },
    { from: 'observation', outcome: 'passed', to: 'acceptance' },
    { from: 'acceptance', outcome: 'passed', to: '$complete' },
    { from: 'acceptance', outcome: 'rejected', to: 'product_spec' },
    { from: 'code_review', outcome: 'rejected', to: 'implementation' },
    { from: 'observation', outcome: 'rejected', to: 'implementation' },
    { from: 'qa', outcome: 'rejected', to: 'implementation' },
    { from: 'tech_design', outcome: 'rejected', to: 'product_spec' },
  ],
  returns: [
    { from: 'tech_design', to: 'product_spec' },
    { from: 'code_review', to: 'implementation' },
    { from: 'qa', to: 'implementation' },
    { from: 'qa', to: 'product_spec' },
    { from: 'observation', to: 'implementation' },
    { from: 'acceptance', to: 'product_spec' },
  ],
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

  it('отделяет возвраты от обратных исходов и делит пучок только прямых рёбер', () => {
    const { edges } = buildLayout(defaultTemplateView);
    const design = edges.filter((edge) => edge.id.includes(':tech_design:'));

    expect(design.filter((edge) => edge.kind === 'forward').map((edge) => edge.label).sort())
      .toEqual(['passed', 'skipped']);
    expect(design.filter((edge) => edge.kind === 'back').map((edge) => edge.label)).toEqual(['rejected']);
    expect(design.filter((edge) => edge.kind === 'return').map((edge) => edge.label)).toEqual(['return_to_product_spec']);

    expect(edges.filter((edge) => edge.kind === 'back').map((edge) => edge.label))
      .toEqual(['rejected', 'rejected', 'rejected', 'rejected', 'rejected']);
    expect(edges.some((edge) => edge.kind === 'return' && edge.label === 'return_to_implementation')).toBe(true);
    expect(edges.some((edge) => edge.kind === 'return' && edge.label === 'rejected')).toBe(false);

    // Два прямых исхода делят пучок симметрично; дуги в счёт не входят.
    const offsets = design.filter((edge) => edge.kind === 'forward').map((edge) => edge.offset);
    expect(offsets.reduce((sum, offset) => sum + offset, 0)).toBe(0);
    expect(new Set(offsets).size).toBe(2);
    const single = edges.find((edge) => edge.id === 'edge:intent:passed:product_spec');
    expect(single?.offset).toBe(0);
  });

  it('разводит подписи рёбер дефолтного шаблона без пересечений', () => {
    const { edges } = buildLayout(defaultTemplateView);
    const collide = (a: typeof edges[number]['labelBox'], b: typeof edges[number]['labelBox']) => a.x < b.x + b.w
      && b.x < a.x + a.w && a.y < b.y + b.h && b.y < a.y + a.h;

    const collisions: string[] = [];
    for (let i = 0; i < edges.length; i += 1) {
      for (let j = i + 1; j < edges.length; j += 1) {
        if (collide(edges[i].labelBox, edges[j].labelBox)) collisions.push(`${edges[i].label} / ${edges[j].label}`);
      }
    }
    expect(collisions).toEqual([]);
  });

  it('рисует возвраты отдельным признаком и красит свотч «Пройден» как пройденный узел', async () => {
    getGraph.mockResolvedValue(defaultTemplateView);
    renderAt('/graphs/sdlc-default');

    const diagram = await screen.findByRole('group', { name: 'Схема графа' });
    const returnGroup = diagram.querySelector('[data-kind="return"]');
    const backGroup = diagram.querySelector('[data-kind="back"]');
    expect(returnGroup).not.toBeNull();
    expect(backGroup).not.toBeNull();
    expect(returnGroup).toHaveClass(styles.edgeReturn);
    expect(returnGroup).not.toHaveClass(styles.edgeBack);
    expect(backGroup).toHaveClass(styles.edgeBack);
    expect(backGroup).not.toHaveClass(styles.edgeReturn);
    expect(within(diagram).getAllByText('return_to_implementation').length).toBeGreaterThan(0);
    expect(screen.getByText('Обратный переход (rejected)')).toBeInTheDocument();
    expect(screen.getByText('Посещён, не пройден')).toBeInTheDocument();
  });
});
