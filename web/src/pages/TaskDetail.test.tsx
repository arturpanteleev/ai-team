import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Link, MemoryRouter, Routes } from '../router';
import type { TaskRunDetails } from '../types';
import { TaskDetail } from './TaskDetail';

const live = vi.hoisted(() => ({ onEvent: undefined as ((event: { run_id: string }) => void) | undefined }));

vi.mock('../hooks/useWebSocket', () => ({
  useWebSocket: (options: { onEvent?: (event: { run_id: string }) => void }) => { live.onEvent = options.onEvent; },
}));

vi.mock('../api', () => ({
  answerTaskGrill: vi.fn().mockResolvedValue({}),
  approveTaskDeliveryPlan: vi.fn().mockResolvedValue({}),
  cancelRun: vi.fn().mockResolvedValue({}),
  confirmTaskStage: vi.fn().mockResolvedValue({}),
  finishTaskGrill: vi.fn().mockResolvedValue({}),
  getPipelineArtifacts: vi.fn().mockResolvedValue([]),
  getRunLog: vi.fn().mockResolvedValue({ content: 'agent log', truncated: false }),
  getTaskRunDetails: vi.fn(),
  getTaskRunEvents: vi.fn().mockResolvedValue({ events: [], next_cursor: 0 }),
  overrideTaskStageCheck: vi.fn().mockResolvedValue({}),
  returnTaskStage: vi.fn().mockResolvedValue({}),
  runTaskStageAgent: vi.fn().mockResolvedValue({}),
  setTaskStageExecutor: vi.fn().mockResolvedValue({}),
  skipTaskStage: vi.fn().mockResolvedValue({}),
  submitTaskStageResult: vi.fn().mockResolvedValue({}),
  takeTaskStage: vi.fn().mockResolvedValue({}),
}));

const baseDetails: TaskRunDetails = {
  run: { id: 5, run_id: 'run-detail-1', feature: 'Новый маршрут', status: 'waiting_for_approval', started_at: '2026-10-12T09:00:00Z' },
  task: 'Сократить путь до первой ценности',
  mode: 'manual',
  external_links: ['JIRA-51', 'https://tracker.example/JIRA-51'],
  template: {
    valid: true,
    template: 'pinned-route',
    title: 'Закреплённый маршрут',
    version: 'a'.repeat(64),
    stages: [
      { id: 'intent', title: 'Идея', function: 'product_owner', result: 'md', executor: 'human' },
      { id: 'analysis', title: 'Анализ', function: 'analyst', result: 'md', executor: 'agent', agent: 'analyst' },
      { id: 'release', title: 'Релиз', function: 'release_manager', result: 'approve', executor: 'human', skippable: true },
    ],
    returns: [{ from: 'release', to: 'intent' }],
  },
  state: { run_id: 'run-detail-1', task: 'Сократить путь до первой ценности', phase: 'waiting', next_stage: 'intent', pending_approval_id: 'input-1', attempt_ordinal: 0 },
  stages: [],
  approvals: [{
    id: 'input-1', run_id: 'run-detail-1', attempt_id: 'input-attempt', from_stage: 'intent', to_stage: 'intent',
    trigger: 'human_input', subject_hash: 'b'.repeat(64), required_roles: ['product_owner'], quorum: 'any',
    actions: ['submit'], status: 'pending', created_at: '2026-10-12T09:00:00Z',
  }],
  stage_owners: {},
  results: { intent: [
    { run_id: 'run-detail-1', artifact_path: 'stages/intent/result.md', revision: 1, id: 'rev-1', content: 'Первая версия', actor_id: 'alice', created_at: '2026-10-12T09:01:00Z', sha256: '1'.repeat(64) },
    { run_id: 'run-detail-1', artifact_path: 'stages/intent/result.md', revision: 2, id: 'rev-2', base_revision: 'rev-1', content: 'Вторая версия', actor_id: 'alice', created_at: '2026-10-12T09:02:00Z', sha256: '2'.repeat(64) },
  ] },
  allowed_actions: ['take', 'results'],
  delivery: { status: 'not_requested' },
};

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={['/tasks/run-detail-1']}>
      <Routes routes={[{ path: '/tasks/:runID', element: <TaskDetail /> }]} />
    </MemoryRouter>,
  );
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

beforeEach(async () => {
  live.onEvent = undefined;
  const api = await import('../api');
  vi.mocked(api.getTaskRunDetails).mockResolvedValue(baseDetails);
  vi.mocked(api.getTaskRunEvents).mockResolvedValue({ events: [{
    version: 1, cursor: 1, run_id: 'run-detail-1', sequence: 1, type: 'run_started',
    timestamp: '2026-10-12T09:00:00Z', data: { mode: 'manual' },
  }], next_cursor: 1 });
});

afterEach(cleanup);

describe('Task detail', () => {
  it('shows the pinned task, result history and comparison, event feed, and only allowed stage actions', async () => {
    renderDetail();

    expect(await screen.findByRole('heading', { name: 'Новый маршрут' })).toBeInTheDocument();
    expect(screen.getByText('Сократить путь до первой ценности')).toBeInTheDocument();
    expect(screen.getByText(/Закреплённый маршрут/)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'https://tracker.example/JIRA-51' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Взять этап' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Сдать результат' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /запустить агента/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Подтвердить этап' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Обойти проверку' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Пропустить этап' })).not.toBeInTheDocument();
    expect(screen.getByText('Задача создана')).toBeInTheDocument();

    fireEvent.click(screen.getByText('Сравнить версии 1 и 2'));
    expect(screen.getAllByText('Первая версия').length).toBeGreaterThan(1);
    expect(screen.getAllByText('Вторая версия').length).toBeGreaterThan(1);
  });

  it('submits the configured human result through the task endpoint', async () => {
    renderDetail();
    fireEvent.click(await screen.findByRole('button', { name: 'Сдать результат' }));
    fireEvent.change(screen.getByLabelText('Результат в Markdown'), { target: { value: 'Проверенная идея' } });
    fireEvent.change(screen.getByLabelText('Что сделано'), { target: { value: 'Описаны ограничения' } });
    fireEvent.click(screen.getByRole('button', { name: 'Отправить результат' }));

    const api = await import('../api');
    await waitFor(() => expect(api.submitTaskStageResult).toHaveBeenCalledWith('run-detail-1', 'intent', {
      result: 'md', content: 'Проверенная идея', description: 'Описаны ограничения',
    }));
    expect(await screen.findByText('Результат отправлен выполнено.')).toBeInTheDocument();
  });

  it('shows the checker report and log link while exposing only confirm when that is allowed', async () => {
    const report: TaskRunDetails = {
      ...baseDetails,
      run: { ...baseDetails.run, status: 'waiting_for_approval' },
      state: { ...baseDetails.state!, next_stage: 'analysis', pending_approval_id: 'check-1' },
      stages: [{
        id: 8, pipeline_run_id: 5, attempt_id: 'agent-attempt-8', stage_index: 1, agent_name: 'analysis',
        status: 'passed', started_at: '2026-10-12T09:10:00Z', completed_at: '2026-10-12T09:12:00Z', duration_ms: 120000,
        checks_json: JSON.stringify([{ name: 'required-sections', class: 'content', command: ['ai-team', 'check'], policy: 'required', status: 'passed', exit_code: 0 }]),
      }],
      approvals: [{
        id: 'check-1', run_id: 'run-detail-1', attempt_id: 'agent-attempt-8', from_stage: 'analysis', to_stage: 'release',
        trigger: 'stage_check', subject_hash: 'c'.repeat(64), required_roles: ['architect'], quorum: 'any', actions: ['approve'], status: 'pending', created_at: '2026-10-12T09:02:00Z',
      }],
      allowed_actions: ['confirm'],
    };
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(report);
    renderDetail();

    expect(await screen.findByRole('button', { name: 'Подтвердить этап' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Взять этап' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Сдать результат' })).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Открыть лог этапа' })).toHaveAttribute(
      'href', '/api/runs/run-detail-1/logs/agent-attempt-8',
    );
    fireEvent.click(screen.getByRole('button', { name: 'Этап analysis: показать детали' }));
    expect(await screen.findByText('Проверки')).toBeInTheDocument();
    expect(screen.getByText('required-sections')).toBeInTheDocument();
  });

  it('renders grill answers and hides done action unless the API allows it', async () => {
    const grill: TaskRunDetails = {
      ...baseDetails,
      state: { ...baseDetails.state!, next_stage: 'analysis', pending_approval_id: 'grill-1' },
      approvals: [{
        id: 'grill-1', run_id: 'run-detail-1', attempt_id: 'attempt-grill', from_stage: 'analysis', to_stage: 'analysis',
        trigger: 'graph_outcome:blocked', subject_hash: 'd'.repeat(64), required_roles: ['product_owner'], quorum: 'any',
        actions: ['answer_questions'], payload: { kind: 'questions', markdown: 'Какой сегмент выбрать?' },
        status: 'pending', created_at: '2026-10-12T09:02:00Z', decisions: [{ approval_id: 'grill-1', actor_id: 'alice', actor_role: 'product_owner', action: 'answer_questions', subject_hash: 'd'.repeat(64), comment: 'Малый бизнес', decided_at: '2026-10-12T09:03:00Z' }],
      }],
      allowed_actions: ['grill/answer'],
    };
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(grill);
    renderDetail();

    expect(await screen.findByRole('heading', { name: 'Грилл: вопросы и ответы' })).toBeInTheDocument();
    expect(screen.getByText('Какой сегмент выбрать?')).toBeInTheDocument();
    expect(screen.getByText('Малый бизнес')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Завершить грилл без ответа' })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Ваш ответ'), { target: { value: 'SMB' } });
    fireEvent.click(screen.getByRole('button', { name: 'Ответить и продолжить' }));
    await waitFor(() => expect(api.answerTaskGrill).toHaveBeenCalledWith('run-detail-1', 'analysis', 'SMB'));
  });

  it('shows the delivery plan content before it can be approved', async () => {
    const planHash = 'e'.repeat(64);
    const plan: TaskRunDetails = {
      ...baseDetails,
      state: { ...baseDetails.state!, next_stage: 'release', pending_approval_id: 'plan-1' },
      approvals: [{
        id: 'plan-1', run_id: 'run-detail-1', attempt_id: 'attempt-release', from_stage: 'release', to_stage: 'release',
        trigger: 'delivery_plan', subject_hash: planHash, required_roles: ['release_manager'], quorum: 'any',
        actions: ['approve', 'reject'], status: 'pending', created_at: '2026-10-12T09:04:00Z',
        payload: {
          schema_version: 1, branch: 'ai-team/run-detail-1', base_branch: 'main', remote: 'origin',
          files: ['web/src/pages/TaskDetail.tsx', 'pkg/web/task_api.go'],
          commit_message: 'feat: task detail', pr_title: 'Страница задачи', pr_body: 'Добавлена панель этапа.',
        },
      }],
      allowed_actions: ['plan/approve'],
    };
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(plan);
    renderDetail();

    const content = await screen.findByRole('region', { name: 'Содержимое плана доставки' });
    expect(content).toHaveTextContent('ai-team/run-detail-1');
    expect(content).toHaveTextContent('main');
    expect(content).toHaveTextContent('web/src/pages/TaskDetail.tsx');
    expect(content).toHaveTextContent('pkg/web/task_api.go');
    expect(content).toHaveTextContent('Страница задачи');
    expect(content).toHaveTextContent('Добавлена панель этапа.');
    fireEvent.click(screen.getByRole('button', { name: 'Подтвердить план' }));
    await waitFor(() => expect(api.approveTaskDeliveryPlan).toHaveBeenCalledWith('run-detail-1', planHash));
  });

  it('keeps resolved grill questions and decisions in task history', async () => {
    const resolved: TaskRunDetails = {
      ...baseDetails,
      approvals: [{
        id: 'grill-done', run_id: 'run-detail-1', attempt_id: 'attempt-grill', from_stage: 'analysis', to_stage: 'analysis',
        trigger: 'graph_outcome:blocked', subject_hash: 'd'.repeat(64), required_roles: ['product_owner'], quorum: 'any',
        actions: ['answer_questions'], payload: { kind: 'questions', markdown: 'Что проверить перед релизом?' },
        status: 'resolved', created_at: '2026-10-12T09:02:00Z', resolved_action: 'answer_questions', resolved_at: '2026-10-12T09:03:00Z',
        decisions: [{ approval_id: 'grill-done', actor_id: 'alice', actor_role: 'product_owner', action: 'answer_questions', subject_hash: 'd'.repeat(64), comment: 'Проверить миграцию базы', decided_at: '2026-10-12T09:03:00Z' }],
      }],
    };
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(resolved);
    renderDetail();

    expect(await screen.findByRole('heading', { name: 'История грилла' })).toBeInTheDocument();
    expect(screen.getByText('Что проверить перед релизом?')).toBeInTheDocument();
    expect(screen.getByText('Проверить миграцию базы')).toBeInTheDocument();
  });

  it('ignores a late previous-run refresh and clears all draft inputs on route change', async () => {
    const api = await import('../api');
    const oldRunDetails: TaskRunDetails = {
      ...baseDetails,
      run: { ...baseDetails.run, run_id: 'old-run', feature: 'Старая задача' },
      task: 'Старое описание',
      state: { ...baseDetails.state!, run_id: 'old-run', next_stage: 'qa', pending_approval_id: 'grill-old' },
      template: { ...baseDetails.template!, stages: [
        ...baseDetails.template!.stages,
        { id: 'qa', title: 'Проверка', function: 'qa', result: 'md', executor: 'human', skippable: true },
      ], returns: [{ from: 'qa', to: 'intent' }] },
      approvals: [{
        id: 'grill-old', run_id: 'old-run', attempt_id: 'qa-attempt', from_stage: 'qa', to_stage: 'qa',
        trigger: 'graph_outcome:blocked', subject_hash: 'd'.repeat(64), required_roles: ['qa'], quorum: 'any',
        actions: ['answer_questions'], payload: { kind: 'questions', markdown: 'Старый вопрос' }, status: 'pending', created_at: '2026-10-12T09:02:00Z',
      }],
      allowed_actions: ['results', 'skip', 'override', 'return', 'grill/answer', 'grill/done'],
    };
    const newRunDetails: TaskRunDetails = {
      ...oldRunDetails,
      run: { ...oldRunDetails.run, run_id: 'new-run', feature: 'Новая задача' },
      task: 'Новое описание',
      state: { ...oldRunDetails.state!, run_id: 'new-run', pending_approval_id: 'grill-new' },
      approvals: [{
        ...oldRunDetails.approvals[0], id: 'grill-new', run_id: 'new-run',
        payload: { kind: 'questions', markdown: 'Новый вопрос' },
      }],
    };
    const oldRefresh = deferred<TaskRunDetails>();
    let oldRunCalls = 0;
    vi.mocked(api.getTaskRunDetails).mockImplementation((runID) => {
      if (runID === 'old-run') {
        oldRunCalls += 1;
        return oldRunCalls === 1 ? Promise.resolve(oldRunDetails) : oldRefresh.promise;
      }
      return Promise.resolve(newRunDetails);
    });
    vi.mocked(api.getTaskRunEvents).mockResolvedValue({ events: [], next_cursor: 0 });

    render(
      <MemoryRouter initialEntries={['/tasks/old-run']}>
        <Link to="/tasks/new-run">Перейти к новой задаче</Link>
        <Routes routes={[{ path: '/tasks/:runID', element: <TaskDetail /> }]} />
      </MemoryRouter>,
    );
    expect(await screen.findByRole('heading', { name: 'Старая задача' })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Сдать результат' }));
    fireEvent.change(screen.getByLabelText('Результат в Markdown'), { target: { value: 'Старый результат' } });
    fireEvent.change(screen.getByLabelText('Причина пропуска'), { target: { value: 'Старый skip' } });
    fireEvent.change(screen.getByLabelText('Причина обхода проверки'), { target: { value: 'Старый override' } });
    fireEvent.change(screen.getByLabelText('Причина возврата'), { target: { value: 'Старый return' } });
    fireEvent.change(screen.getByLabelText('Ваш ответ'), { target: { value: 'Старый ответ' } });

    await act(async () => {
      live.onEvent?.({ run_id: 'old-run' });
      await Promise.resolve();
    });
    await waitFor(() => expect(oldRunCalls).toBe(2));
    fireEvent.click(screen.getByRole('link', { name: 'Перейти к новой задаче' }));
    expect(await screen.findByRole('heading', { name: 'Новая задача' })).toBeInTheDocument();
    expect(screen.queryByText('Старое описание')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Сдать результат' }));
    expect(screen.getByLabelText('Результат в Markdown')).toHaveValue('');
    expect(screen.getByLabelText('Причина пропуска')).toHaveValue('');
    expect(screen.getByLabelText('Причина обхода проверки')).toHaveValue('');
    expect(screen.getByLabelText('Причина возврата')).toHaveValue('');
    expect(screen.getByLabelText('Ваш ответ')).toHaveValue('');

    await act(async () => {
      oldRefresh.resolve(oldRunDetails);
      await oldRefresh.promise;
    });
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Новая задача' })).toBeInTheDocument());
    expect(screen.queryByRole('heading', { name: 'Старая задача' })).not.toBeInTheDocument();
    expect(screen.getByLabelText('Результат в Markdown')).toHaveValue('');
  });
});
