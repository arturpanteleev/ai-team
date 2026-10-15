import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter, Routes } from '../router';
import type { TaskEvent, TaskRunDetails } from '../types';
import { TaskDetail } from './TaskDetail';

vi.mock('../hooks/useWebSocket', () => ({ useWebSocket: () => undefined }));

vi.mock('../api', () => ({
  answerTaskGrill: vi.fn(), approveTaskDeliveryPlan: vi.fn(), cancelRun: vi.fn(), confirmTaskStage: vi.fn(), decideApproval: vi.fn(),
  finishTaskGrill: vi.fn(), getActivePrincipal: vi.fn().mockReturnValue(null), getArtifact: vi.fn().mockResolvedValue(''),
  getPipelineArtifacts: vi.fn().mockResolvedValue([]),
  getPipelineRun: vi.fn().mockResolvedValue({ run: { id: 5, run_id: 'run-detail-1' }, stages: [] }),
  resumeRun: vi.fn(), retryDelivery: vi.fn(), getRunLog: vi.fn().mockResolvedValue({ content: '', truncated: false }),
  getTaskGates: vi.fn(), getTaskRunDetails: vi.fn(), retryTaskGate: vi.fn(),
  getTaskRunEvents: vi.fn(),
  overrideTaskStageCheck: vi.fn(), returnTaskStage: vi.fn(), runTaskStageAgent: vi.fn(), setTaskStageExecutor: vi.fn(),
  skipTaskStage: vi.fn(), submitTaskGrillVersion: vi.fn(), submitTaskStageResult: vi.fn(), takeTaskStage: vi.fn(),
  isAuthenticationRequired: () => true,
  getTaskFlowMetrics: vi.fn().mockResolvedValue({ by_stage: [] }),
  getRunTracker: vi.fn(), getTrackerOverview: vi.fn(),
  setRunTrackerLink: vi.fn(), removeRunTrackerLink: vi.fn(), retryRunTrackerDeliveries: vi.fn(), dismissTrackerInbound: vi.fn(), syncTrackerNow: vi.fn(),
}));

const baseDetails: TaskRunDetails = {
  run: { id: 5, run_id: 'run-detail-1', feature: 'Маршрут', status: 'waiting_for_approval', started_at: '2026-10-12T09:00:00Z' },
  task: 'Сократить путь',
  mode: 'manual',
  template: {
    valid: true, template: 'route', title: 'Маршрут', version: 'a'.repeat(64),
    stages: [
      { id: 'intent', title: 'Идея', function: 'product_owner', result: 'md', executor: 'human' },
      { id: 'analysis', title: 'Анализ', function: 'analyst', result: 'md', executor: 'agent', agent: 'analyst', skippable: true },
    ],
    returns: [{ from: 'analysis', to: 'intent' }],
  },
  state: { run_id: 'run-detail-1', task: 'Сократить путь', phase: 'waiting', next_stage: 'analysis', pending_approval_id: 'c1', attempt_ordinal: 1 },
  stages: [],
  approvals: [{
    id: 'c1', run_id: 'run-detail-1', attempt_id: 'a1', from_stage: 'analysis', to_stage: 'analysis', trigger: 'stage_check',
    subject_hash: 'c'.repeat(64), required_roles: ['product_owner'], quorum: 'any', actions: ['approve', 'reject'], targets: { approve: 'intent' },
    status: 'pending', created_at: '2026-10-12T09:00:00Z',
  }],
  stage_owners: {},
  results: {},
  allowed_actions: ['confirm', 'skip', 'return'],
  delivery: { status: 'not_requested' },
};

const configuredTracker = {
  configured: true, tracker: 'fake', sync_status: 'ok',
  link: { task_id: 5, tracker: 'fake', issue_key: 'DEMO-1', linked_by: 'owner', linked_at: '2026-10-12T09:00:00Z' },
  conflicts: [], deliveries: [], inbound: [],
};

function renderDetail() {
  return render(
    <MemoryRouter initialEntries={['/tasks/run-detail-1']}>
      <Routes routes={[{ path: '/tasks/:runID', element: <TaskDetail /> }]} />
    </MemoryRouter>,
  );
}

function manyEvents(count: number): TaskEvent[] {
  return Array.from({ length: count }, (_, index) => ({
    version: 1, cursor: index + 1, run_id: 'run-detail-1', sequence: index + 1, type: 'attempt_started',
    timestamp: '2026-10-12T09:00:00Z', data: { line: `очень длинная строка лога ${'x'.repeat(200)} ${index}` },
  }));
}

beforeEach(async () => {
  const api = await import('../api');
  vi.mocked(api.getActivePrincipal).mockReturnValue({ actor_id: 'owner', roles: ['product_owner'] });
  vi.mocked(api.getTaskRunDetails).mockReset().mockResolvedValue(baseDetails);
  vi.mocked(api.getTaskGates).mockReset().mockResolvedValue({ run_id: 'run-detail-1', can_override: false, stages: [], overrides: [], warnings: [] });
  vi.mocked(api.getTaskRunEvents).mockReset().mockResolvedValue({ events: [], next_cursor: 0 });
  vi.mocked(api.getRunTracker).mockReset().mockResolvedValue({ configured: false, sync_status: 'not_configured' });
  vi.mocked(api.getTrackerOverview).mockReset().mockResolvedValue({ configured: false });
});

afterEach(cleanup);

describe('Task page columns keep their purpose (S16)', () => {
  it('puts the state in the left column and the allowed actions in the right one', async () => {
    renderDetail();
    const context = await screen.findByRole('complementary', { name: 'Статус и контекст' });
    const actions = screen.getByRole('complementary', { name: 'Действия' });

    expect(within(context).getByText('Где задача')).toBeInTheDocument();
    expect(within(context).getByText('Кто действует')).toBeInTheDocument();
    expect(within(context).queryByRole('button', { name: /Принять|Пропустить|Вернуть|Отклонить/ })).not.toBeInTheDocument();
    expect(within(actions).getByRole('button', { name: /Принять и перейти/ })).toBeInTheDocument();
    expect(within(actions).getByRole('button', { name: 'Пропустить этап' })).toBeInTheDocument();
    expect(within(actions).getByRole('button', { name: 'Вернуть на доработку' })).toBeInTheDocument();
    expect(within(context).queryByLabelText('Причина пропуска')).not.toBeInTheDocument();
  });

  it('keeps one button per meaning: no stage action is offered twice', async () => {
    renderDetail();
    const actions = await screen.findByRole('complementary', { name: 'Действия' });
    const names = within(actions).getAllByRole('button').map((button) => button.textContent?.trim());
    expect(new Set(names).size).toBe(names.length);
    // The confirm action and the matching decision are one «Принять и перейти» — not «Подтвердить» next to «Принять».
    expect(names.filter((name) => /Принять и перейти/.test(name ?? ''))).toHaveLength(1);
    expect(screen.queryByRole('button', { name: /Подтвердить/ })).not.toBeInTheDocument();
  });

  it('does not grow the actions column when the log, artifacts and events are huge', async () => {
    const api = await import('../api');
    renderDetail();
    const actions = await screen.findByRole('complementary', { name: 'Действия' });
    const before = actions.textContent;
    const buttonsBefore = within(actions).getAllByRole('button').length;

    cleanup();
    vi.mocked(api.getTaskRunEvents).mockResolvedValue({ events: manyEvents(300), next_cursor: 300 });
    vi.mocked(api.getPipelineArtifacts).mockResolvedValue(Array.from({ length: 200 }, (_, index) => ({
      name: `file-${index}.md`, path: `attempts/a1/artifacts/file-${index}.md`, run_id: 'run-detail-1', size: 10, mod_time: '2026-10-12T09:00:00Z',
    })));
    renderDetail();
    const afterColumn = await screen.findByRole('complementary', { name: 'Действия' });
    fireEvent.click(await screen.findByRole('tab', { name: 'Журнал' }));
    expect(await screen.findByText(/очень длинная строка лога/, { selector: 'pre' }, { timeout: 100 }).catch(() => null)).toBeDefined();

    expect(afterColumn.textContent).toBe(before);
    expect(within(afterColumn).getAllByRole('button')).toHaveLength(buttonsBefore);
    expect(within(afterColumn).queryByText(/очень длинная строка лога/)).not.toBeInTheDocument();
  });

  it('moves the grill questions to the main area and keeps only the answer field in the actions', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({
      ...baseDetails,
      state: { ...baseDetails.state!, pending_approval_id: 'g1' },
      approvals: [{
        id: 'g1', run_id: 'run-detail-1', attempt_id: 'a1', from_stage: 'analysis', to_stage: 'analysis', trigger: 'grill',
        subject_hash: 'd'.repeat(64), required_roles: ['product_owner'], quorum: 'any', actions: ['answer', 'new_version', 'done'],
        payload: { kind: 'grill_questions', markdown: 'Какой сегмент проверяем первым?' }, status: 'pending', created_at: '2026-10-12T09:00:00Z',
      }],
      allowed_actions: ['grill/answer', 'grill/done'],
    });
    renderDetail();
    const question = await screen.findByText('Какой сегмент проверяем первым?');
    const actions = screen.getByRole('complementary', { name: 'Действия' });
    expect(actions).not.toContainElement(question);
    expect(within(actions).getByLabelText('Ваш ответ')).toBeInTheDocument();
    expect(within(actions).getByRole('button', { name: 'Завершить грилл без ответа' })).toBeInTheDocument();
  });

  it('shows the delivery plan in the main area and only its hash and the accept button in the actions', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({
      ...baseDetails,
      state: { ...baseDetails.state!, pending_approval_id: 'p1' },
      approvals: [{
        id: 'p1', run_id: 'run-detail-1', attempt_id: 'a1', from_stage: 'analysis', to_stage: 'analysis', trigger: 'delivery_plan',
        subject_hash: 'e'.repeat(64), required_roles: ['release_manager'], quorum: 'any', actions: ['approve', 'reject'],
        payload: { branch: 'ai-team/x', base_branch: 'main', files: Array.from({ length: 40 }, (_, index) => `docs/file-${index}.md`) },
        status: 'pending', created_at: '2026-10-12T09:00:00Z',
      }],
      allowed_actions: ['plan/approve'],
    });
    vi.mocked((await import('../api')).getActivePrincipal).mockReturnValue({ actor_id: 'rm', roles: ['release_manager'] });
    renderDetail();
    const plan = await screen.findByRole('region', { name: 'Содержимое плана доставки' });
    const actions = screen.getByRole('complementary', { name: 'Действия' });
    expect(actions).not.toContainElement(plan);
    expect(within(actions).getByRole('button', { name: 'Принять план доставки' })).toBeInTheDocument();
    expect(actions).not.toHaveTextContent('docs/file-39.md');
  });
});

describe('Task page tabs (S16)', () => {
  it('switches tabs with arrow keys, Home and End and keeps only the active tab in the tab order', async () => {
    renderDetail();
    const tablist = await screen.findByRole('tablist', { name: 'Материалы задачи' });
    const tabs = within(tablist).getAllByRole('tab');
    expect(tabs.map((tab) => tab.getAttribute('tabindex'))).toEqual(['0', '-1', '-1', '-1', '-1']);

    tabs[0].focus();
    fireEvent.keyDown(tabs[0], { key: 'ArrowRight' });
    expect(tabs[1]).toHaveAttribute('aria-selected', 'true');
    expect(tabs[1]).toHaveFocus();
    fireEvent.keyDown(tabs[1], { key: 'End' });
    expect(tabs.at(-1)).toHaveAttribute('aria-selected', 'true');
    fireEvent.keyDown(tabs.at(-1)!, { key: 'ArrowRight' });
    expect(tabs[0]).toHaveAttribute('aria-selected', 'true');
    fireEvent.keyDown(tabs[0], { key: 'ArrowLeft' });
    expect(tabs.at(-1)).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('tabpanel')).toHaveAttribute('aria-labelledby', tabs.at(-1)!.id);
  });

  it('marks the gates tab with a red dot for an override and an amber one for a recommendation', async () => {
    const api = await import('../api');
    const wave = {
      stage_id: 'analysis', visit: 1, stage_attempt_id: 'a1', artifact_sha256: 'a'.repeat(64), current: true, attempts: [],
      gates: [{ gate_name: 'review', gate: 'review', gate_version: 1, mode: 'required', group: 0, attempts: 1, blocking: false, warning: false,
        retryable: false, overridden: true, attempt: { attempt_id: 'g1', gate: 'review', gate_name: 'review', mode: 'required', group: 0, execution: 'succeeded', verdict: 'fail',
          started_at: '2026-10-12T09:10:00Z' } }],
      transition: { allowed: true, state: 'overridden', reasons: [], blocking: [], warnings: [], retryable: [], overridden: ['review'] },
      override: { stage: 'analysis', scope: 'a1', artifact_sha256: 'a'.repeat(64), reason: 'срочно', actor_id: 'root', authority: 'system_admin',
        at: '2026-10-12T09:20:00Z', gates: [{ gate_name: 'review', attempt_id: 'g1', execution: 'succeeded', verdict: 'fail' }] },
    };
    vi.mocked(api.getTaskGates).mockResolvedValue({ run_id: 'run-detail-1', can_override: false, overrides: [], warnings: [],
      stages: [{ stage_id: 'analysis', title: 'Анализ', groups: [['review']], waves: [wave], current: wave }] } as never);
    renderDetail();
    const gatesTab = await screen.findByRole('tab', { name: /Гейты/ });
    expect(within(gatesTab).getByLabelText('есть обход провала')).toHaveAttribute('data-flag', 'danger');
  });
});

describe('Task page tracker (S14/S15)', () => {
  async function withTracker(data: unknown) {
    const api = await import('../api');
    vi.mocked(api.getRunTracker).mockResolvedValue(data as never);
    vi.mocked(api.getTrackerOverview).mockResolvedValue({ configured: true, tracker: 'fake', inbound: { configured: true, poll_interval_seconds: 60, status: 'ok' } });
    return api;
  }

  it('shows the linked card in the status column, the actions in the right one and the details in a tab', async () => {
    const api = await withTracker({ ...configuredTracker, sync_status: 'inbound_pending',
      inbound: [{ tracker: 'fake', change_id: 'chg-1', task_id: 5, issue_key: 'DEMO-1', from_status: 'To Do', to_status: 'In Review', origin: 'external',
        changed_at: '2026-10-12T09:00:00Z', state: 'pending', action: 'advance', reason: 'ждёт принятия этапа',
        detected_at: '2026-10-12T09:00:00Z', updated_at: '2026-10-12T09:00:00Z' }] });
    renderDetail();

    const summary = await screen.findByTestId('tracker-summary');
    expect(within(screen.getByRole('complementary', { name: 'Статус и контекст' })).getByTestId('tracker-summary')).toBe(summary);
    expect(summary).toHaveTextContent('DEMO-1');
    expect(summary).toHaveTextContent('Есть входящий запрос трекера');
    const actions = screen.getByRole('complementary', { name: 'Действия' });
    expect(within(actions).getByRole('button', { name: 'Снять связь' })).toBeInTheDocument();
    expect(within(actions).getByRole('button', { name: 'Синхронизировать сейчас' })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('tab', { name: 'Трекер' }));
    expect(screen.getByRole('tabpanel')).toHaveTextContent('ждёт принятия этапа');
    fireEvent.change(screen.getByLabelText('Причина закрытия'), { target: { value: 'не нужно' } });
    fireEvent.click(screen.getByRole('button', { name: 'Закрыть запрос без действий' }));
    await waitFor(() => expect(api.dismissTrackerInbound).toHaveBeenCalledWith('run-detail-1', 'chg-1', 'не нужно'));
  });

  it('has no tracker tab and no tracker actions when no tracker is configured', async () => {
    renderDetail();
    await screen.findByRole('tablist');
    expect(screen.queryByRole('tab', { name: 'Трекер' })).not.toBeInTheDocument();
    expect(screen.queryByTestId('tracker-summary')).not.toBeInTheDocument();
  });

  it('does not offer tracker writes to a developer', async () => {
    const api = await withTracker(configuredTracker);
    vi.mocked(api.getActivePrincipal).mockReturnValue({ actor_id: 'dev', roles: ['developer'] });
    renderDetail();
    const actions = await screen.findByRole('complementary', { name: 'Действия' });
    expect(await within(actions).findByText(/Связь с карточкой меняют: Владелец продукта, Архитектор/)).toBeInTheDocument();
    expect(within(actions).queryByRole('button', { name: 'Снять связь' })).not.toBeInTheDocument();
  });
});

describe('Task page by role (S16)', () => {
  it('shows a developer who may decide nothing why, instead of an empty column', async () => {
    const api = await import('../api');
    vi.mocked(api.getActivePrincipal).mockReturnValue({ actor_id: 'dev', roles: ['developer'] });
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({ ...baseDetails, allowed_actions: [] });
    renderDetail();
    const actions = await screen.findByRole('complementary', { name: 'Действия' });
    expect(await within(actions).findByText(/Ответить или решить может участник с ролью: product_owner/)).toBeInTheDocument();
    expect(within(actions).queryByRole('button', { name: /Принять и перейти/ })).not.toBeInTheDocument();
  });

  it('says plainly that nothing is available and names the roles of the viewer', async () => {
    const api = await import('../api');
    vi.mocked(api.getActivePrincipal).mockReturnValue({ actor_id: 'rev', roles: ['reviewer'] });
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({ ...baseDetails, run: { ...baseDetails.run, status: 'running' }, approvals: [], allowed_actions: [] });
    renderDetail();
    const note = await screen.findByTestId('no-actions');
    expect(note).toHaveTextContent('Сейчас у вас нет доступных действий');
    expect(note).toHaveTextContent('Ваши роли: Ревьюер');
  });

  it('offers the override only to an administrator and only once', async () => {
    const api = await import('../api');
    vi.mocked(api.getActivePrincipal).mockReturnValue({ actor_id: 'root', roles: ['product_owner', 'admin'] });
    vi.mocked(api.getTaskGates).mockResolvedValue({ run_id: 'run-detail-1', can_override: true, stages: [], overrides: [], warnings: [] });
    vi.mocked(api.getTaskRunDetails).mockResolvedValue({ ...baseDetails, allowed_actions: ['override'] });
    renderDetail();
    const actions = await screen.findByRole('complementary', { name: 'Действия' });
    expect(await within(actions).findAllByRole('button', { name: 'Обойти проверку' })).toHaveLength(1);
  });
});
