import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Link, MemoryRouter, Routes } from '../router';
import type { Stage, TaskRunDetails } from '../types';
import { DRAFT_SAVE_DELAY_MS } from '../components/StageResultForm';
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
  decideApproval: vi.fn().mockResolvedValue({}),
  finishTaskGrill: vi.fn().mockResolvedValue({}),
  getActivePrincipal: vi.fn().mockReturnValue(null),
  getArtifact: vi.fn().mockResolvedValue(''),
  getPipelineArtifacts: vi.fn().mockResolvedValue([]),
  getPipelineRun: vi.fn().mockResolvedValue({ run: { id: 5, run_id: 'run-detail-1' }, stages: [] }),
  resumeRun: vi.fn().mockResolvedValue({}),
  retryDelivery: vi.fn().mockResolvedValue({}),
  getRunLog: vi.fn().mockResolvedValue({ content: 'agent log', truncated: false }),
  getTaskRunDetails: vi.fn(),
  getTaskRunEvents: vi.fn().mockResolvedValue({ events: [], next_cursor: 0 }),
  getTaskFlowMetrics: vi.fn().mockResolvedValue({ by_stage: [] }),
  overrideTaskStageCheck: vi.fn().mockResolvedValue({}),
  returnTaskStage: vi.fn().mockResolvedValue({}),
  runTaskStageAgent: vi.fn().mockResolvedValue({}),
  setTaskStageExecutor: vi.fn().mockResolvedValue({}),
  skipTaskStage: vi.fn().mockResolvedValue({}),
  submitTaskGrillVersion: vi.fn().mockResolvedValue({}),
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

class MemoryStorage implements Storage {
  private values = new Map<string, string>();
  failWrites = false;
  get length() { return this.values.size; }
  clear() { this.values.clear(); }
  getItem(key: string) { return this.values.get(key) ?? null; }
  key(index: number) { return [...this.values.keys()][index] ?? null; }
  removeItem(key: string) { this.values.delete(key); }
  setItem(key: string, value: string) {
    if (this.failWrites) throw new DOMException('quota exceeded', 'QuotaExceededError');
    this.values.set(key, value);
  }
}

let storage: MemoryStorage;

function useStorage(value: () => Storage) {
  Object.defineProperty(window, 'localStorage', { configurable: true, get: value });
}

beforeEach(async () => {
  live.onEvent = undefined;
  storage = new MemoryStorage();
  useStorage(() => storage);
  const api = await import('../api');
  vi.mocked(api.getTaskRunDetails).mockResolvedValue(baseDetails);
  vi.mocked(api.submitTaskStageResult).mockReset().mockResolvedValue({});
  vi.mocked(api.getTaskRunEvents).mockResolvedValue({ events: [{
    version: 1, cursor: 1, run_id: 'run-detail-1', sequence: 1, type: 'run_started',
    timestamp: '2026-10-12T09:00:00Z', data: { mode: 'manual' },
  }], next_cursor: 1 });
});

afterEach(cleanup);

describe('Task detail', () => {
  it('shows the pinned task, result history and comparison, event feed, and only allowed stage actions', async () => {
    renderDetail();

    expect(await screen.findByRole('heading', { name: 'Бизнес-идея' })).toBeInTheDocument();
    expect(screen.getByText('Сократить путь до первой ценности')).toBeInTheDocument();
    expect(screen.getAllByText(/Закреплённый маршрут/).length).toBeGreaterThan(0);
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
    expect(await screen.findByText('Результат принят')).toBeInTheDocument();
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
    expect(content).not.toHaveTextContent('В плане нет ни ветки');
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

  it('submits a new grill version with an optional comment when the API allows it', async () => {
    const grill: TaskRunDetails = {
      ...baseDetails,
      state: { ...baseDetails.state!, next_stage: 'analysis', pending_approval_id: 'grill-2' },
      approvals: [{
        id: 'grill-2', run_id: 'run-detail-1', attempt_id: 'attempt-grill', from_stage: 'analysis', to_stage: 'analysis',
        trigger: 'grill', subject_hash: 'd'.repeat(64), required_roles: ['analyst'], quorum: 'any',
        actions: ['answer', 'new_version', 'done'], payload: { kind: 'grill_questions', markdown: 'Нет раздела рисков' },
        status: 'pending', created_at: '2026-10-12T09:02:00Z',
      }],
      allowed_actions: ['grill/new-version'],
    };
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(grill);
    renderDetail();

    expect(await screen.findByRole('heading', { name: 'Грилл: вопросы и ответы' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Ответить и продолжить' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Отправить новую версию' }));
    await waitFor(() => expect(api.submitTaskGrillVersion).toHaveBeenLastCalledWith('run-detail-1', 'analysis', ''));
    fireEvent.change(screen.getByLabelText('Комментарий к новой версии'), { target: { value: ' Добавил риски ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Отправить новую версию' }));
    await waitFor(() => expect(api.submitTaskGrillVersion).toHaveBeenLastCalledWith('run-detail-1', 'analysis', 'Добавил риски'));

    cleanup();
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({ ...grill, allowed_actions: ['grill/answer', 'grill/done'] });
    renderDetail();
    expect(await screen.findByRole('button', { name: 'Ответить и продолжить' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Отправить новую версию' })).not.toBeInTheDocument();
  });

  it('returns to the first route of the current stage and forgets a target picked on a previous stage', async () => {
    const stages = [
      ...baseDetails.template!.stages,
      { id: 'qa', title: 'Проверка', function: 'qa', result: 'md' as const, executor: 'human' as const },
    ];
    const atStage = (stage: string): TaskRunDetails => ({
      ...baseDetails,
      template: { ...baseDetails.template!, stages, returns: [
        { from: 'release', to: 'intent' }, { from: 'release', to: 'analysis' },
        { from: 'qa', to: 'intent' }, { from: 'qa', to: 'analysis' },
      ] },
      state: { ...baseDetails.state!, next_stage: stage },
      allowed_actions: ['return'],
    });
    const api = await import('../api');
    vi.mocked(api.returnTaskStage).mockClear();
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(atStage('release'));
    renderDetail();

    fireEvent.change(await screen.findByLabelText('Причина возврата'), { target: { value: 'Не тот объём' } });
    fireEvent.click(screen.getByRole('button', { name: 'Вернуть этап' }));
    await waitFor(() => expect(api.returnTaskStage).toHaveBeenLastCalledWith('run-detail-1', 'release', 'intent', 'Не тот объём'));

    fireEvent.change(screen.getByLabelText('Вернуть на этап'), { target: { value: 'analysis' } });
    expect(screen.getByLabelText('Вернуть на этап')).toHaveValue('analysis');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(atStage('qa'));
    await act(async () => {
      live.onEvent?.({ run_id: 'run-detail-1' });
      await Promise.resolve();
    });
    expect(await screen.findByRole('heading', { name: 'Проверка', level: 2 })).toBeInTheDocument();
    expect(screen.getByLabelText('Вернуть на этап')).toHaveValue('intent');
    fireEvent.change(screen.getByLabelText('Причина возврата'), { target: { value: 'Повторить' } });
    fireEvent.click(screen.getByRole('button', { name: 'Вернуть этап' }));
    await waitFor(() => expect(api.returnTaskStage).toHaveBeenLastCalledWith('run-detail-1', 'qa', 'intent', 'Повторить'));
  });

  it('shows when the responsible person took the stage', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({
      ...baseDetails,
      stage_owners: { intent: { stage_id: 'intent', approval_id: 'input-1', actor_id: 'alice', actor_role: 'product_owner', taken_at: '2026-10-12T09:05:00Z' } },
    });
    renderDetail();

    const owner = await screen.findByText(/Ответственный: alice/);
    expect(owner).toHaveTextContent('взял этап');
    expect(owner.querySelector('time')).toHaveAttribute('dateTime', '2026-10-12T09:05:00Z');
  });

  it('does not turn a scheme-only external link into a clickable link', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({ ...baseDetails, external_links: ['https:tracker.example/JIRA-52'] });
    renderDetail();

    expect(await screen.findByText('https:tracker.example/JIRA-52')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'https:tracker.example/JIRA-52' })).not.toBeInTheDocument();
  });

  it('warns when the delivery plan payload is an empty object', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({
      ...baseDetails,
      state: { ...baseDetails.state!, next_stage: 'release', pending_approval_id: 'plan-empty' },
      approvals: [{
        id: 'plan-empty', run_id: 'run-detail-1', attempt_id: 'attempt-release', from_stage: 'release', to_stage: 'release',
        trigger: 'delivery_plan', subject_hash: 'e'.repeat(64), required_roles: ['release_manager'], quorum: 'any',
        actions: ['approve', 'reject'], status: 'pending', created_at: '2026-10-12T09:04:00Z', payload: {},
      }],
      allowed_actions: ['plan/approve'],
    });
    renderDetail();

    const content = await screen.findByRole('region', { name: 'Содержимое плана доставки' });
    expect(content).toHaveTextContent('В плане нет ни ветки, ни файлов, ни описания PR');
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
    expect(await screen.findByText('Старое описание')).toBeInTheDocument();

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
    expect(await screen.findByText('Новое описание')).toBeInTheDocument();
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
    await waitFor(() => expect(screen.getByText('Новое описание')).toBeInTheDocument());
    expect(screen.queryByText('Старое описание')).not.toBeInTheDocument();
    expect(screen.getByLabelText('Результат в Markdown')).toHaveValue('');
  });
});

function detailsAt(stage: TaskRunDetails['template'] extends infer T ? T extends { stages: Array<infer S> } ? S : never : never): TaskRunDetails {
  return {
    ...baseDetails,
    template: { ...baseDetails.template!, stages: [stage] },
    state: { ...baseDetails.state!, next_stage: stage.id },
    approvals: [{ ...baseDetails.approvals[0], from_stage: stage.id, to_stage: stage.id }],
    results: {},
  };
}

const draftKey = (stage = 'intent') => `ai-team:stage-result-draft:v1:run-detail-1:${stage}`;

function hidePage() {
  act(() => { window.dispatchEvent(new Event('pagehide')); });
}

async function openResultForm() {
  renderDetail();
  fireEvent.click(await screen.findByRole('button', { name: 'Сдать результат' }));
}

describe('Browser stage submission', () => {
  it('previews markdown, submits it, and drops the draft after the server accepts it', async () => {
    await openResultForm();
    fireEvent.change(screen.getByLabelText('Результат в Markdown'), { target: { value: '# План\n\nшаги' } });
    fireEvent.change(screen.getByLabelText('Что сделано'), { target: { value: 'Собран план' } });
    hidePage();
    expect(JSON.parse(storage.getItem(draftKey())!)).toMatchObject({ content: '# План\n\nшаги', description: 'Собран план' });

    fireEvent.click(screen.getByRole('button', { name: 'Предпросмотр' }));
    expect(screen.getByRole('heading', { name: 'План', level: 1 })).toBeInTheDocument();
    expect(screen.queryByLabelText('Результат в Markdown')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Отправить результат' }));

    const api = await import('../api');
    await waitFor(() => expect(api.submitTaskStageResult).toHaveBeenCalledWith('run-detail-1', 'intent', {
      result: 'md', content: '# План\n\nшаги', description: 'Собран план',
    }));
    expect(await screen.findByText('Результат принят')).toBeInTheDocument();
    expect(storage.getItem(draftKey())).toBeNull();
    expect(screen.queryByRole('button', { name: 'Предпросмотр' })).not.toBeInTheDocument();
  });

  it('loads a markdown file into the editor and rejects files over the server limit', async () => {
    await openResultForm();
    const input = screen.getByLabelText('Загрузить файл .md');
    fireEvent.change(input, { target: { files: [new File(['# Из файла\n'], 'result.md', { type: 'text/markdown' })] } });
    await waitFor(() => expect(screen.getByLabelText('Результат в Markdown')).toHaveValue('# Из файла\n'));
    expect(screen.getByText('Загружен файл: result.md')).toBeInTheDocument();

    const tooLarge = new File([new Uint8Array(10 * 1024 * 1024 + 1)], 'huge.md', { type: 'text/markdown' });
    fireEvent.change(input, { target: { files: [tooLarge] } });
    expect(await screen.findByText(/Файл huge\.md больше 10\.0 МиБ/)).toBeInTheDocument();
    expect(screen.getByLabelText('Результат в Markdown')).toHaveValue('# Из файла\n');

    fireEvent.change(input, { target: { files: [new File([new Uint8Array([0xff, 0xfe, 0x00])], 'binary.md')] } });
    expect(await screen.findByText('Файл binary.md должен быть текстом в UTF-8.')).toBeInTheDocument();
  });

  it('blocks markdown larger than the server limit in bytes, not characters', async () => {
    await openResultForm();
    // 5.5M Cyrillic characters fit a character limit but are 11 MB of UTF-8.
    fireEvent.change(screen.getByLabelText('Результат в Markdown'), { target: { value: 'я'.repeat(5_500_000) } });
    expect(screen.getByRole('alert')).toHaveTextContent('Результат больше 10.0 МиБ');
    expect(screen.getByRole('button', { name: 'Отправить результат' })).toBeDisabled();
  });

  it('submits a link with the stage link kind after validating the URL', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(detailsAt(
      { id: 'deploy', title: 'Выкладка', function: 'deployer', result: 'link', link_kind: 'build', executor: 'human' }));
    await openResultForm();
    expect(screen.getByText('Вид ссылки: Сборка')).toBeInTheDocument();
    const url = screen.getByLabelText('Ссылка на результат');
    const submit = screen.getByRole('button', { name: 'Отправить результат' });
    expect(submit).toBeDisabled();

    fireEvent.change(url, { target: { value: 'ftp://ci.example/build/7' } });
    expect(screen.getByRole('alert')).toHaveTextContent('Ссылка должна начинаться с http:// или https://.');
    expect(submit).toBeDisabled();
    fireEvent.change(url, { target: { value: 'ci.example/build/7' } });
    expect(screen.getByRole('alert')).toHaveTextContent('Введите полный адрес');
    fireEvent.change(url, { target: { value: 'https://bot:secret@ci.example/build/7' } });
    expect(screen.getByRole('alert')).toHaveTextContent('Уберите логин и пароль из ссылки.');
    fireEvent.change(url, { target: { value: ` https://ci.example/${'a'.repeat(16 * 1024)} ` } });
    expect(screen.getByRole('alert')).toHaveTextContent('Ссылка длиннее 16384 байт.');

    fireEvent.change(url, { target: { value: ' https://ci.example/build/7 ' } });
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Что сделано'), { target: { value: 'Выложено на стенд' } });
    fireEvent.click(submit);
    await waitFor(() => expect(api.submitTaskStageResult).toHaveBeenCalledWith('run-detail-1', 'deploy', {
      result: 'link', url: 'https://ci.example/build/7', link_kind: 'build', description: 'Выложено на стенд',
    }));
  });

  it('hints when a pull-request link does not point to a pull request', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(detailsAt(
      { id: 'implementation', title: 'Реализация', function: 'developer', result: 'link', link_kind: 'pr', executor: 'human' }));
    await openResultForm();
    fireEvent.change(screen.getByLabelText('Ссылка на результат'), { target: { value: 'https://github.com/org/repo' } });
    expect(screen.getByText(/не похожа на pull request/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Ссылка на результат'), { target: { value: 'https://github.com/org/repo/pull/12' } });
    expect(screen.queryByText(/не похожа на pull request/)).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Отправить результат' })).toBeEnabled();
  });

  it('approves with confirmation text', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(detailsAt(
      { id: 'release', title: 'Релиз', function: 'release_manager', result: 'approve', executor: 'human' }));
    await openResultForm();
    expect(screen.queryByLabelText('Результат в Markdown')).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Текст подтверждения'), { target: { value: 'Релиз согласован' } });
    fireEvent.change(screen.getByLabelText('Что сделано'), { target: { value: 'Проверен чек-лист' } });
    fireEvent.click(screen.getByRole('button', { name: 'Подтвердить результат' }));
    await waitFor(() => expect(api.submitTaskStageResult).toHaveBeenCalledWith('run-detail-1', 'release', {
      result: 'approve', note: 'Релиз согласован', description: 'Проверен чек-лист',
    }));
  });

  it('warns about an empty "what was done" field but still submits without it', async () => {
    await openResultForm();
    const warning = /Поле «Что сделано» пустое/;
    expect(screen.getByText(warning)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Что сделано'), { target: { value: '   ' } });
    expect(screen.getByText(warning)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Результат в Markdown'), { target: { value: 'Итог' } });
    fireEvent.click(screen.getByRole('button', { name: 'Отправить результат' }));
    const api = await import('../api');
    await waitFor(() => expect(api.submitTaskStageResult).toHaveBeenCalledWith('run-detail-1', 'intent', { result: 'md', content: 'Итог' }));

    cleanup();
    await openResultForm();
    fireEvent.change(screen.getByLabelText('Что сделано'), { target: { value: 'Описано' } });
    expect(screen.queryByText(warning)).not.toBeInTheDocument();
  });

  it('restores the draft after a page reload and reopens the form', async () => {
    await openResultForm();
    fireEvent.change(screen.getByLabelText('Результат в Markdown'), { target: { value: 'Черновик идеи' } });
    fireEvent.change(screen.getByLabelText('Что сделано'), { target: { value: 'Половина' } });
    fireEvent.change(screen.getByLabelText('Заметка'), { target: { value: 'Вернуться после обеда' } });
    cleanup();

    renderDetail();
    expect(await screen.findByLabelText('Результат в Markdown')).toHaveValue('Черновик идеи');
    expect(screen.getByLabelText('Что сделано')).toHaveValue('Половина');
    expect(screen.getByLabelText('Заметка')).toHaveValue('Вернуться после обеда');
    expect(screen.getByText('Восстановлен черновик, сохранённый в этом браузере.')).toBeInTheDocument();

    const confirm = vi.spyOn(window, 'confirm').mockReturnValueOnce(false).mockReturnValueOnce(true);
    fireEvent.click(screen.getByRole('button', { name: 'Очистить черновик' }));
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(screen.getByLabelText('Результат в Markdown')).toHaveValue('Черновик идеи');
    expect(storage.getItem(draftKey())).not.toBeNull();

    fireEvent.click(screen.getByRole('button', { name: 'Очистить черновик' }));
    expect(confirm).toHaveBeenCalledTimes(2);
    expect(screen.getByLabelText('Результат в Markdown')).toHaveValue('');
    expect(storage.getItem(draftKey())).toBeNull();
    confirm.mockRestore();
  });

  it('keeps the draft when the server rejects the submission', async () => {
    const api = await import('../api');
    vi.mocked(api.submitTaskStageResult).mockRejectedValueOnce(new Error('human markdown is missing required section "## Цель"'));
    await openResultForm();
    fireEvent.change(screen.getByLabelText('Результат в Markdown'), { target: { value: 'Без раздела' } });
    fireEvent.click(screen.getByRole('button', { name: 'Отправить результат' }));

    expect(await screen.findByText('human markdown is missing required section "## Цель"')).toBeInTheDocument();
    expect(screen.getByLabelText('Результат в Markdown')).toHaveValue('Без раздела');
    expect(JSON.parse(storage.getItem(draftKey())!)).toMatchObject({ content: 'Без раздела' });

    cleanup();
    renderDetail();
    expect(await screen.findByLabelText('Результат в Markdown')).toHaveValue('Без раздела');
  });

  it('works without browser storage and says the draft is not saved', async () => {
    useStorage(() => { throw new DOMException('blocked', 'SecurityError'); });
    await openResultForm();
    fireEvent.change(screen.getByLabelText('Результат в Markdown'), { target: { value: 'Без хранилища' } });
    hidePage();
    expect(screen.getByText(/Черновик не удаётся сохранить в браузере/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Отправить результат' }));
    const api = await import('../api');
    await waitFor(() => expect(api.submitTaskStageResult).toHaveBeenCalledWith('run-detail-1', 'intent', { result: 'md', content: 'Без хранилища' }));
    expect(await screen.findByText('Результат принят')).toBeInTheDocument();
  });

  it('keeps editing when the storage quota is exceeded', async () => {
    storage.failWrites = true;
    await openResultForm();
    fireEvent.change(screen.getByLabelText('Результат в Markdown'), { target: { value: 'Большой текст' } });
    hidePage();
    expect(screen.getByLabelText('Результат в Markdown')).toHaveValue('Большой текст');
    expect(screen.getByText(/Черновик не удаётся сохранить в браузере/)).toBeInTheDocument();
  });

  it('batches draft writes while typing and flushes them when the page is hidden', async () => {
    await openResultForm();
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    try {
      const editor = screen.getByLabelText('Результат в Markdown');
      fireEvent.change(editor, { target: { value: 'Первый' } });
      act(() => { vi.advanceTimersByTime(DRAFT_SAVE_DELAY_MS - 1); });
      expect(storage.getItem(draftKey())).toBeNull();

      fireEvent.change(editor, { target: { value: 'Первый абзац' } });
      act(() => { vi.advanceTimersByTime(DRAFT_SAVE_DELAY_MS - 1); });
      expect(storage.getItem(draftKey())).toBeNull();
      act(() => { vi.advanceTimersByTime(1); });
      expect(JSON.parse(storage.getItem(draftKey())!)).toMatchObject({ content: 'Первый абзац' });

      fireEvent.change(editor, { target: { value: 'Первый абзац и второй' } });
      hidePage();
      expect(JSON.parse(storage.getItem(draftKey())!)).toMatchObject({ content: 'Первый абзац и второй' });
    } finally {
      vi.useRealTimers();
    }
  });

  it('keeps the latest text when the form is collapsed before the debounce fires', async () => {
    await openResultForm();
    fireEvent.change(screen.getByLabelText('Результат в Markdown'), { target: { value: 'Свернул сразу' } });
    fireEvent.click(screen.getByRole('button', { name: 'Свернуть' }));
    expect(JSON.parse(storage.getItem(draftKey())!)).toMatchObject({ content: 'Свернул сразу' });
  });

  it('does not write the draft back after an accepted submission', async () => {
    await openResultForm();
    fireEvent.change(screen.getByLabelText('Результат в Markdown'), { target: { value: 'Сдаю без паузы' } });
    fireEvent.click(screen.getByRole('button', { name: 'Отправить результат' }));
    expect(await screen.findByText('Результат принят')).toBeInTheDocument();
    expect(storage.getItem(draftKey())).toBeNull();
    cleanup();
    expect(storage.getItem(draftKey())).toBeNull();
  });

  it('locks the form while sending and never writes the draft back after an accepted submission', async () => {
    const api = await import('../api');
    const accepted = deferred<unknown>();
    vi.mocked(api.submitTaskStageResult).mockReturnValueOnce(accepted.promise);
    await openResultForm();
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    try {
      fireEvent.change(screen.getByLabelText('Результат в Markdown'), { target: { value: 'Отправляемый текст' } });
      fireEvent.change(screen.getByLabelText('Что сделано'), { target: { value: 'Готово' } });
      fireEvent.click(screen.getByRole('button', { name: 'Отправить результат' }));
      expect(screen.getByLabelText('Результат в Markdown')).toBeDisabled();
      expect(screen.getByLabelText('Что сделано')).toBeDisabled();
      expect(screen.getByLabelText('Заметка')).toBeDisabled();
      expect(screen.getByLabelText('Загрузить файл .md')).toBeDisabled();

      await act(async () => {
        accepted.resolve({});
        await accepted.promise;
      });
      vi.useRealTimers();
      expect(await screen.findByText('Результат принят')).toBeInTheDocument();
      expect(storage.getItem(draftKey())).toBeNull();
      hidePage();
      cleanup();
      expect(storage.getItem(draftKey())).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it('keeps the form open and the draft stored after a rejected submission and saves new edits', async () => {
    const api = await import('../api');
    vi.mocked(api.submitTaskStageResult).mockRejectedValueOnce(new Error('rejected'));
    await openResultForm();
    fireEvent.change(screen.getByLabelText('Результат в Markdown'), { target: { value: 'Первая попытка' } });
    fireEvent.click(screen.getByRole('button', { name: 'Отправить результат' }));
    expect(await screen.findByText('rejected')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Результат в Markdown'), { target: { value: 'Вторая попытка' } });
    cleanup();
    expect(JSON.parse(storage.getItem(draftKey())!)).toMatchObject({ content: 'Вторая попытка' });
  });

  it('drops the stored older draft when the newer one cannot be written', async () => {
    storage.setItem(draftKey(), JSON.stringify({ content: 'Старая версия', url: '', note: '', description: '' }));
    await openResultForm();
    expect(screen.getByLabelText('Результат в Markdown')).toHaveValue('Старая версия');
    storage.failWrites = true;
    fireEvent.change(screen.getByLabelText('Результат в Markdown'), { target: { value: 'Новая версия' } });
    hidePage();
    expect(storage.getItem(draftKey())).toBeNull();
    expect(screen.getByText(/Черновик не удаётся сохранить в браузере/)).toBeInTheDocument();
  });

  it('clears an empty draft without asking for confirmation', async () => {
    const confirm = vi.spyOn(window, 'confirm');
    await openResultForm();
    fireEvent.click(screen.getByRole('button', { name: 'Очистить черновик' }));
    expect(confirm).not.toHaveBeenCalled();
    confirm.mockRestore();
  });

  it('ignores a corrupted stored draft', async () => {
    storage.setItem(draftKey(), '{not json');
    await openResultForm();
    expect(screen.getByLabelText('Результат в Markdown')).toHaveValue('');
    expect(screen.queryByText(/Восстановлен черновик/)).not.toBeInTheDocument();
  });
});

describe('Task page controls moved from the old pipeline page', () => {
  it('shows what happens after confirmation and the next step in one line', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({
      ...baseDetails,
      run: { ...baseDetails.run, status: 'running' },
      state: { ...baseDetails.state!, phase: 'running', pending_approval_id: undefined },
      approvals: [],
      allowed_actions: ['confirm'],
    });
    renderDetail();

    expect(await screen.findByText('Что произойдёт после подтверждения: Анализ')).toBeInTheDocument();
    expect(screen.getAllByText('Ожидает').length).toBeGreaterThan(0);
    expect(screen.getByRole('button', { name: 'Подтвердить этап' })).toBeInTheDocument();
  });

  it('offers the manual path and the agent path as separate choices with the route states', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({
      ...baseDetails,
      state: { ...baseDetails.state!, next_stage: 'analysis', pending_approval_id: undefined },
      approvals: [],
      allowed_actions: ['take', 'results', 'run-agent'],
    });
    renderDetail();

    expect(await screen.findByRole('heading', { name: 'Сделаю сам' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Агент analyst подготовит результат' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Запустить агента' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Взять этап' })).toBeInTheDocument();
    expect(screen.getAllByText('пройден')).toHaveLength(1);
    expect(screen.getAllByText('текущий')).toHaveLength(1);
    expect(screen.getAllByText('впереди')).toHaveLength(1);
  });

  it('keeps service fields out of the main view until the details block is opened', async () => {
    renderDetail();
    await screen.findByRole('heading', { name: 'Бизнес-идея' });

    const details = screen.getAllByText('Подробности', { selector: 'summary' })[0].closest('details');
    expect(details).not.toBeNull();
    expect(details).toHaveTextContent('run-detail-1');
    expect(details).toHaveTextContent('Длительность');
  });

  it('shows the intention text on the page and not only as a file link', async () => {
    const api = await import('../api');
    vi.mocked(api.getPipelineArtifacts).mockResolvedValue([
      { name: '0001-intention.md', path: 'brief/0001-intention.md', run_id: 'run-detail-1', size: 10, mod_time: '2026-10-12T09:00:00Z' },
    ]);
    vi.mocked(api.getArtifact).mockResolvedValue('Клиенты входят через Google без пароля.');
    renderDetail();

    expect(await screen.findByText('Клиенты входят через Google без пароля.')).toBeInTheDocument();
    expect(api.getArtifact).toHaveBeenCalledWith('run-detail-1', 'brief/0001-intention.md');
    vi.mocked(api.getPipelineArtifacts).mockResolvedValue([]);
  });

  it('decides a plain approval through the task API and shows where it leads', async () => {
    const api = await import('../api');
    const approval = {
      id: 'spec-1', run_id: 'run-detail-1', attempt_id: 'attempt-spec', from_stage: 'intent', to_stage: 'intent',
      trigger: 'stage_gate', subject_hash: 'f'.repeat(64), required_roles: ['product_owner'], quorum: 'any' as const,
      actions: ['approve', 'reject'], targets: { approve: 'analysis' }, status: 'pending' as const, created_at: '2026-10-12T09:00:00Z',
    };
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({
      ...baseDetails,
      state: { ...baseDetails.state!, pending_approval_id: 'spec-1' },
      approvals: [approval],
      allowed_actions: [],
    });
    renderDetail();

    expect(await screen.findByText('Далее: Согласовать → Анализ')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Согласовать' }));
    await waitFor(() => expect(api.decideApproval).toHaveBeenCalledWith('run-detail-1', approval, expect.objectContaining({
      action: 'approve', actor_role: 'product_owner', actor_id: 'local-user',
    })));
  });

  it('resumes a stopped task and retries a failed delivery from the same page', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({
      ...baseDetails,
      run: { ...baseDetails.run, status: 'stopped' },
      approvals: [],
      allowed_actions: [],
    });
    renderDetail();
    fireEvent.click(await screen.findByRole('button', { name: 'Продолжить задачу' }));
    await waitFor(() => expect(api.resumeRun).toHaveBeenCalledWith('run-detail-1'));
    expect(screen.queryByRole('button', { name: 'Выдать бюджет и продолжить' })).not.toBeInTheDocument();

    cleanup();
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({
      ...baseDetails,
      run: { ...baseDetails.run, status: 'completed' },
      approvals: [],
      allowed_actions: [],
      delivery: { status: 'failed', error: 'push rejected' },
    });
    renderDetail();
    fireEvent.click(await screen.findByRole('button', { name: 'Повторить одобренную Git-доставку' }));
    await waitFor(() => expect(api.retryDelivery).toHaveBeenCalledWith('run-detail-1'));
  });
  it('moves the back link out of the header and names the idea block without the intention wording', async () => {
    renderDetail();
    await screen.findByRole('heading', { name: 'Бизнес-идея' });

    expect(screen.queryByRole('link', { name: '← К задачам' })).not.toBeInTheDocument();
    expect(screen.getByText('Бизнес-идея')).toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 2, name: 'Бизнес-идея' })).toBeInTheDocument();
    expect(screen.queryByText('Исходное намерение')).not.toBeInTheDocument();
    expect(screen.queryByText(/Бизнес-намерение/)).not.toBeInTheDocument();
  });

  const agentStage = (status: 'running' | 'failed', extra: Partial<Stage> = {}): Stage => ({
    id: 9, pipeline_run_id: 5, attempt_id: 'agent-attempt-9', stage_index: 1, agent_name: 'analysis',
    status, started_at: new Date().toISOString(), ...extra,
  });
  const agentOnStage = (stages: Stage[]): TaskRunDetails => ({
    ...baseDetails,
    state: { ...baseDetails.state!, next_stage: 'analysis', pending_approval_id: undefined,
      executor_overrides: { analysis: { executor: 'agent', previous_executor: 'human' } } },
    approvals: [],
    stages,
    allowed_actions: ['run-agent', 'take', 'results'],
  });

  it('shows the agent working state with a link to its log', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(agentOnStage([agentStage('running')]));
    renderDetail();

    expect(await screen.findByText('Агент работает…')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Открыть лог агента' })).toHaveAttribute(
      'href', '/api/runs/run-detail-1/logs/agent-attempt-9',
    );
  });

  it('shows the agent failure with its reason', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(agentOnStage([agentStage('failed', { error: 'Нет доступа к репозиторию' })]));
    renderDetail();

    expect(await screen.findByText('Этап «Анализ» упал')).toBeInTheDocument();
    expect(screen.getByTestId('stage-failure')).toHaveTextContent('Нет доступа к репозиторию');
  });

  it('confirms the agent start at once, before the server reports the attempt', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({ ...agentOnStage([]) });
    renderDetail();

    fireEvent.click(await screen.findByRole('button', { name: 'Запустить агента' }));
    expect(await screen.findByText('Агент запущен')).toBeInTheDocument();
    expect(api.runTaskStageAgent).toHaveBeenCalledWith('run-detail-1', 'analysis', 'run');
  });
  it('marks the failed stage in the route and shows its short reason with the agent log on the current stage', async () => {
    const api = await import('../api');
    const failed = agentStage('failed', {
      error: 'Не найден файл спецификации\nНужен docs/spec.md в ветке\nхвост трассировки, которого не должно быть на странице',
    });
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({
      ...agentOnStage([failed]),
      run: { ...baseDetails.run, status: 'failed' },
      allowed_actions: [],
    });
    renderDetail();

    const banner = await screen.findByTestId('stage-failure');
    expect(banner).toHaveTextContent('Этап «Анализ» упал');
    expect(banner).toHaveTextContent('Не найден файл спецификации Нужен docs/spec.md в ветке');
    expect(banner).not.toHaveTextContent('хвост трассировки');
    expect(within(banner).getByRole('link', { name: 'Открыть лог агента' })).toHaveAttribute(
      'href', '/api/runs/run-detail-1/logs/agent-attempt-9',
    );
    expect(screen.getByText('Упал')).toBeInTheDocument();
    expect(screen.getByText('Упал').closest('li')).toHaveAttribute('data-state', 'failed');
  });

  it('shows the run error when a failed run has no stage attempt to point at', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({
      ...baseDetails,
      run: { ...baseDetails.run, status: 'failed', error: 'Сборка упала на шаге проверки' },
      stages: [],
      allowed_actions: [],
    });
    renderDetail();

    const banner = await screen.findByTestId('stage-failure');
    expect(banner).toHaveTextContent('Прогон завершился ошибкой');
    expect(banner).toHaveTextContent('Сборка упала на шаге проверки');
    expect(within(banner).queryByRole('link')).not.toBeInTheDocument();
  });
  it('answers the product questions inside the current stage card and shows the wait while the server answers', async () => {
    const api = await import('../api');
    const question = {
      id: 'q-1', run_id: 'run-detail-1', attempt_id: 'attempt-q', from_stage: 'intent', to_stage: 'intent',
      trigger: 'human_input', subject_hash: 'e'.repeat(64), required_roles: ['product_owner'], quorum: 'any' as const,
      actions: ['answer_questions'], payload: { kind: 'questions', markdown: 'Какой сегмент выбрать?' },
      status: 'pending' as const, created_at: '2026-10-12T09:00:00Z',
    };
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({
      ...baseDetails,
      state: { ...baseDetails.state!, next_stage: 'intent', pending_approval_id: 'q-1' },
      approvals: [question],
      allowed_actions: ['results'],
    });
    vi.mocked(api.decideApproval).mockImplementationOnce(() => new Promise(() => undefined) as never);
    renderDetail();

    expect(await screen.findByRole('heading', { name: 'Нужно уточнение Product Owner' })).toBeInTheDocument();
    const card = document.getElementById('current-stage');
    expect(card).toHaveTextContent('Какой сегмент выбрать?');
    fireEvent.change(screen.getAllByLabelText('Ответ на вопрос')[0], { target: { value: 'Малый бизнес' } });
    fireEvent.click(within(card!).getByRole('button', { name: 'Ответить и продолжить' }));

    expect(await screen.findByText('Ждём ответ сервера…')).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: 'Сохраняем…' })[0]).toBeDisabled();
  });
  it('moves the launch notice to the working state once the server reports the agent attempt', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(agentOnStage([]));
    renderDetail();

    fireEvent.click(await screen.findByRole('button', { name: 'Запустить агента' }));
    vi.mocked(api.getTaskRunDetails).mockResolvedValue(agentOnStage([agentStage('running')]));
    expect(await screen.findByText('Агент работает…')).toBeInTheDocument();
    expect(screen.queryByText('Агент запущен')).not.toBeInTheDocument();
  });
  it('lists the pending approval of the current stage with its actions and asks for a decision', async () => {
    const api = await import('../api');
    const pendingInput = {
      id: 'input-2', run_id: 'run-detail-1', attempt_id: 'attempt-input', from_stage: 'intent', to_stage: 'intent',
      trigger: 'human_input', subject_hash: 'f'.repeat(64), required_roles: ['product_owner'], quorum: 'any' as const,
      actions: ['submit', 'run_agent', 'refine_agent', 'reject'], targets: {}, status: 'pending' as const,
      created_at: '2026-10-12T09:00:00Z',
    };
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({
      ...baseDetails,
      state: { ...baseDetails.state!, next_stage: 'intent', pending_approval_id: 'input-2' },
      approvals: [pendingInput],
      allowed_actions: ['take', 'results', 'run-agent'],
    });
    renderDetail();

    expect(await screen.findByText(/Ждёт вашего решения/)).toBeInTheDocument();
    const approvals = document.getElementById('approvals-heading')!.closest('section')!;
    expect(within(approvals).getByRole('button', { name: 'Запустить агента' })).toBeInTheDocument();
    expect(within(approvals).getByRole('button', { name: 'Отклонить' })).toBeInTheDocument();
    expect(within(approvals).queryByRole('button', { name: 'submit' })).not.toBeInTheDocument();
  });
});
