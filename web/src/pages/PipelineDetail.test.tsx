import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from '../router';
import { decideApproval } from '../api';
import { PipelineDetail } from './PipelineDetail';

const session = vi.hoisted(() => ({ principal: null as null | { actor_id: string; roles: ('product_owner' | 'developer')[] } }));

vi.mock('../hooks/useWebSocket', () => ({
  useWebSocket: () => ({ connected: true }),
}));

afterEach(() => { cleanup(); session.principal = null; });

vi.mock('../api', () => ({
  getActivePrincipal: () => session.principal,
  getPipelineRun: vi.fn().mockResolvedValue({
    run: {
      id: 7, run_id: 'run-graph', feature: 'graph-feature',
      status: 'waiting_for_approval', started_at: '2026-07-28T00:00:00Z',
    },
    stages: [],
    approvals: [{
      id: 'approval-question', run_id: 'run-graph', attempt_id: 'attempt-1',
      from_stage: 'analyst', to_stage: 'analyst', trigger: 'graph_outcome:blocked',
      subject_hash: 'a'.repeat(64), required_roles: ['product_owner'], quorum: 'any',
      actions: ['answer_questions', 'stop'], targets: { answer_questions: 'analyst', stop: '$stop' },
      payload: { kind: 'questions', markdown: 'Кто целевой клиент?' }, status: 'pending',
    }, {
      id: 'approval-spec', run_id: 'run-graph', attempt_id: 'attempt-2',
      from_stage: 'analyst', to_stage: 'architect', trigger: 'graph_outcome:passed',
      subject_hash: 'b'.repeat(64), required_roles: ['product_owner'], quorum: 'any',
      actions: ['approve_spec', 'reject'], targets: { approve_spec: 'architect', reject: '$stop' },
      payload: { kind: 'agreed_spec', brief_version: { id: 'brief-abcd', sha256: 'c'.repeat(64) }, artifacts: { proposal: 'd'.repeat(64), spec: 'e'.repeat(64) } },
      status: 'pending',
    }],
    next_stage: 'architect',
  }),
  getPipelineArtifacts: vi.fn().mockResolvedValue([
    { name: 'proposal.md', path: 'attempts/attempt-2/artifacts/feat/proposal.md', run_id: 'run-graph', size: 20, mod_time: '' },
    { name: 'spec.md', path: 'attempts/attempt-2/artifacts/feat/specs/product/spec.md', run_id: 'run-graph', size: 20, mod_time: '' },
    { name: '0001-intention.md', path: 'brief/0001-intention.md', run_id: 'run-graph', size: 20, mod_time: '' },
  ]),
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

  it('показывает вопрос analyst как форму и передаёт текст ответа в durable decision API', async () => {
    render(<MemoryRouter initialEntries={['/pipelines/7']}><PipelineDetail /></MemoryRouter>);

    expect(await screen.findByText('Вопрос аналитика')).toBeInTheDocument();
    expect(screen.getAllByText('Кто целевой клиент?').length).toBeGreaterThan(0);
    const answer = screen.getAllByLabelText('Ваш ответ')[0];
    fireEvent.change(answer, { target: { value: 'B2B-клиенты среднего бизнеса' } });
    fireEvent.click(screen.getByRole('button', { name: 'Ответить и продолжить' }));

    expect(vi.mocked(decideApproval)).toHaveBeenCalledWith('run-graph', expect.objectContaining({
      id: 'approval-question',
    }), expect.objectContaining({
      action: 'answer_questions', comment: 'B2B-клиенты среднего бизнеса', actor_role: 'product_owner',
    }));
  });

  it('показывает историю намерения и документы с явным согласованием до архитектора', async () => {
    render(<MemoryRouter initialEntries={['/pipelines/7']}><PipelineDetail /></MemoryRouter>);

    expect(await screen.findByText('История бизнес-намерения')).toBeInTheDocument();
    expect(screen.getByText('0001-intention.md')).toBeInTheDocument();
    expect(screen.getByText('Product Owner согласует требования перед архитектором')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Открыть обоснование' })).toHaveAttribute(
      'href', '/artifacts/run-graph/attempts/attempt-2/artifacts/feat/proposal.md',
    );
    expect(screen.getByRole('link', { name: 'Открыть product spec' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Согласовать ТЗ и передать архитектору' })).toBeInTheDocument();
  });

  it('в облачном UI оставляет согласование ТЗ только Product Owner', async () => {
    session.principal = { actor_id: 'dev-1', roles: ['developer'] };
    render(<MemoryRouter initialEntries={['/pipelines/7']}><PipelineDetail /></MemoryRouter>);

    expect(await screen.findByText('Product Owner согласует требования перед архитектором')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Согласовать ТЗ и передать архитектору' })).not.toBeInTheDocument();
  });
});
