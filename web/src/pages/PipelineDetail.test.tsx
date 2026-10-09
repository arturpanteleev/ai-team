import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter, Routes } from '../router';
import { decideApproval, getPipelineRun, resumeRun, retryDelivery, takeStage } from '../api';
import { PipelineDetail } from './PipelineDetail';

const session = vi.hoisted(() => ({ principal: null as null | { actor_id: string; roles: ('product_owner' | 'developer')[] } }));

vi.mock('../hooks/useWebSocket', () => ({
  useWebSocket: () => ({ connected: true }),
}));

afterEach(() => { cleanup(); session.principal = null; });

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={['/pipelines/7']}>
      <Routes routes={[{ path: '/pipelines/:id', element: <PipelineDetail /> }]} />
    </MemoryRouter>,
  );
}

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
      subject_hash: 'b'.repeat(64), required_roles: ['product_owner'], quorum: 'all',
      actions: ['approve_spec', 'reject'], targets: { approve_spec: 'architect', reject: '$stop' },
      payload: { kind: 'agreed_spec', brief_version: { id: 'brief-abcd', sha256: 'c'.repeat(64) }, artifacts: { proposal: 'd'.repeat(64), spec: 'e'.repeat(64) } },
      artifact_revisions: { 'attempts/attempt-2/artifacts/feat/spec.md': 'rev-000001-A' },
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
  getRunTemplateVersion: vi.fn().mockResolvedValue({ version: 'f'.repeat(64) }),
  decideApproval: vi.fn(),
  resumeRun: vi.fn(),
  cancelRun: vi.fn(),
  retryDelivery: vi.fn(),
  takeStage: vi.fn().mockResolvedValue({ owner: { stage_id: 'product_spec', actor_id: 'bob@example.test', actor_role: 'product_owner', taken_at: '2026-10-09T01:00:00Z' }, changed: true }),
}));

describe('PipelineDetail graph', () => {
  it('показывает команды задачи только в подходящем состоянии', async () => {
    vi.mocked(getPipelineRun).mockResolvedValueOnce({
      run: {
        id: 7, run_id: 'run-graph', feature: 'graph-feature',
        status: 'completed', started_at: '2026-07-28T00:00:00Z', completed_at: '2026-07-28T00:01:00Z',
      },
      stages: [],
      approvals: [],
    });
    renderDetail();

    expect(await screen.findByRole('heading', { name: 'graph-feature' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Продолжить задачу' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Отменить задачу' })).not.toBeInTheDocument();
  });

  it('показывает immutable graph, policy и текущий узел', async () => {
    renderDetail();

    expect(await screen.findByText('Маршрут workflow')).toBeInTheDocument();
    expect(screen.getByText('architect · max 2')).toHaveAttribute('data-current', 'true');
    expect(screen.getByText(/product_owner · quorum any/)).toHaveTextContent('approve→architect');
  });

  it('показывает ответственного и время владения human-этапом, позволяет перехватить и обновляет задачу', async () => {
    session.principal = { actor_id: 'bob@example.test', roles: ['product_owner'] };
    vi.mocked(getPipelineRun).mockClear();
    vi.mocked(takeStage).mockClear();
    const waitingHumanInput = {
      run: { id: 7, run_id: 'run-graph', feature: 'graph-feature', status: 'waiting_for_approval' as const, started_at: '2026-07-28T00:00:00Z' },
      stages: [],
      approvals: [{
        id: 'approval-human-input', run_id: 'run-graph', attempt_id: 'attempt-human',
        from_stage: 'product_spec', to_stage: 'product_spec', trigger: 'human_input',
        subject_hash: 'a'.repeat(64), required_roles: ['product_owner'], quorum: 'any' as const,
        actions: ['submit', 'reject'], payload: { kind: 'input', stage_id: 'product_spec' }, status: 'pending' as const,
        created_at: '2026-10-09T00:00:00Z',
      }],
      next_stage: 'product_spec',
      stage_owners: { product_spec: { stage_id: 'product_spec', approval_id: 'approval-human-input', actor_id: 'alice@example.test', actor_role: 'product_owner', taken_at: '2026-10-09T00:00:00Z' } },
    };
    vi.mocked(getPipelineRun).mockResolvedValueOnce(waitingHumanInput).mockResolvedValueOnce(waitingHumanInput);
    vi.mocked(takeStage).mockResolvedValue({
      owner: { stage_id: 'product_spec', approval_id: 'approval-human-input', actor_id: 'bob@example.test', actor_role: 'product_owner', taken_at: '2026-10-09T01:00:00Z' },
      changed: true,
    });
    renderDetail();

    expect(await screen.findByRole('heading', { name: 'Ответственный за этап «product_spec»' })).toBeInTheDocument();
    expect(screen.getByRole('region', { name: 'Ответственный за этап «product_spec»' }))
      .toHaveTextContent(/alice@example.test держит этап/);
    fireEvent.click(screen.getByRole('button', { name: 'Перехватить этап' }));
    await waitFor(() => expect(takeStage).toHaveBeenCalledWith('run-graph', 'product_spec'));
    await waitFor(() => expect(getPipelineRun).toHaveBeenCalledTimes(2));
  });

  it('показывает вопрос analyst как форму и передаёт текст ответа в durable decision API', async () => {
    renderDetail();

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
    renderDetail();

    expect(await screen.findByText('История бизнес-намерения')).toBeInTheDocument();
    expect(screen.getByText('0001-intention.md')).toBeInTheDocument();
    expect(screen.getByText('Product Owner согласует требования перед архитектором')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Открыть обоснование' })).toHaveAttribute(
      'href', '/artifacts/run-graph/attempts/attempt-2/artifacts/feat/proposal.md',
    );
    expect(screen.getByRole('link', { name: 'Открыть спецификацию продукта' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Согласовать ТЗ и передать архитектору' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Продолжить задачу' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Отменить задачу' })).toBeInTheDocument();
  });

  it('в облачном UI оставляет согласование ТЗ только Product Owner', async () => {
    session.principal = { actor_id: 'dev-1', roles: ['developer'] };
    renderDetail();

    expect(await screen.findByText('Product Owner согласует требования перед архитектором')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Согласовать ТЗ и передать архитектору' })).not.toBeInTheDocument();
  });

  it('повторно отправляет закреплённые human revisions вместе с голосом quorum', async () => {
    renderDetail();

    expect(await screen.findByText('Product Owner согласует требования перед архитектором')).toBeInTheDocument();
    expect(screen.getByText('Закреплённые версии для всех голосов')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Согласовать ТЗ и передать архитектору' }));

    expect(vi.mocked(decideApproval)).toHaveBeenCalledWith('run-graph', expect.objectContaining({
      id: 'approval-spec', artifact_revisions: { 'attempts/attempt-2/artifacts/feat/spec.md': 'rev-000001-A' },
    }), expect.objectContaining({
      action: 'approve_spec', artifact_revisions: { 'attempts/attempt-2/artifacts/feat/spec.md': 'rev-000001-A' },
    }));
  });

  it('сохраняет решение по ТЗ и продолжает тот же run', async () => {
    vi.mocked(getPipelineRun).mockClear();
    vi.mocked(decideApproval).mockClear();
    vi.mocked(resumeRun).mockClear();
    renderDetail();

    expect(await screen.findByText('Product Owner согласует требования перед архитектором')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Согласовать ТЗ и передать архитектору' }));
    await waitFor(() => expect(vi.mocked(decideApproval)).toHaveBeenCalledWith('run-graph', expect.objectContaining({
      id: 'approval-spec',
    }), expect.objectContaining({ action: 'approve_spec', actor_role: 'product_owner' })));

    fireEvent.click(screen.getByRole('button', { name: 'Продолжить задачу' }));
    await waitFor(() => expect(vi.mocked(resumeRun)).toHaveBeenCalledWith('run-graph'));
    await waitFor(() => expect(vi.mocked(getPipelineRun).mock.calls.length).toBeGreaterThanOrEqual(3));
  });

  it('показывает зафиксированные коммит и pull request в состоянии Git-доставки', async () => {
    vi.mocked(getPipelineRun).mockResolvedValue({
      run: { id: 7, run_id: 'run-graph', feature: 'graph-feature', status: 'completed', started_at: '2026-07-28T00:00:00Z' },
      stages: [],
      delivery: { status: 'recorded', record: {
        schema_version: 1, run_id: 'run-graph', feature: 'graph-feature', plan_hash: 'a'.repeat(64),
        commit_sha: 'b'.repeat(40), pr_url: 'https://example.test/pr/42', performed_at: '2026-07-28T00:01:00Z',
      } },
    });
    renderDetail();

    expect(await screen.findByRole('heading', { name: 'Git-доставка' })).toBeInTheDocument();
    expect(screen.getByText('Статус: recorded')).toBeInTheDocument();
    expect(screen.getByText('b'.repeat(40))).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'https://example.test/pr/42' })).toHaveAttribute('href', 'https://example.test/pr/42');
    expect(screen.queryByRole('button', { name: 'Повторить одобренную Git-доставку' })).not.toBeInTheDocument();
    expect(retryDelivery).not.toHaveBeenCalled();
  });

  it('повторяет доставку, перечитывает durable receipt после сбоя ответа и ограничивает повтор по роли', async () => {
    const pendingRun = {
      run: { id: 7, run_id: 'run-graph', feature: 'graph-feature', status: 'completed', started_at: '2026-07-28T00:00:00Z' },
      stages: [], delivery: { status: 'pending' },
    } as Awaited<ReturnType<typeof getPipelineRun>>;
    const deliveredRun = {
      run: pendingRun.run, stages: [], delivery: { status: 'recorded', record: {
        schema_version: 1, run_id: 'run-graph', feature: 'graph-feature', plan_hash: 'a'.repeat(64),
        commit_sha: 'b'.repeat(40), pr_url: 'https://example.test/pr/44', performed_at: '2026-07-28T00:01:00Z',
      } },
    } as Awaited<ReturnType<typeof getPipelineRun>>;
    vi.mocked(getPipelineRun).mockResolvedValueOnce(pendingRun).mockResolvedValueOnce(deliveredRun);
    vi.mocked(retryDelivery).mockRejectedValueOnce(new Error('connection reset after request'));
    renderDetail();

    fireEvent.click(await screen.findByRole('button', { name: 'Повторить одобренную Git-доставку' }));
    expect(retryDelivery).toHaveBeenCalledWith('run-graph');
    expect(await screen.findByRole('alert')).toHaveTextContent('Статус доставки обновлён.');
    expect(screen.getByText('Статус: recorded')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'https://example.test/pr/44' })).toHaveAttribute('href', 'https://example.test/pr/44');
    expect(screen.queryByRole('button', { name: 'Повторить одобренную Git-доставку' })).not.toBeInTheDocument();

    cleanup();
    session.principal = { actor_id: 'dev-1', roles: ['developer'] };
    vi.mocked(getPipelineRun).mockResolvedValue(pendingRun);
    renderDetail();
    expect(await screen.findByText('Статус: pending')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Повторить одобренную Git-доставку' })).not.toBeInTheDocument();
  });

});
