import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Tasks } from './Tasks';
import { getProjects, getTaskRuns, getTemplatesList } from '../api';
import type { TaskRun } from '../types';

const ws = vi.hoisted(() => ({ onEvent: undefined as ((event: unknown) => void) | undefined }));
const route = vi.hoisted(() => ({ search: '', setParams: vi.fn() }));

vi.mock('../hooks/useWebSocket', () => ({
  useWebSocket: (options: { onEvent?: (event: unknown) => void }) => {
    ws.onEvent = options.onEvent;
  },
}));
vi.mock('../router', () => ({
  Link: ({ to, children }: { to: string; children: ReactNode }) => <a href={to}>{children}</a>,
  useNavigate: () => () => {},
  useSearchParams: () => [new URLSearchParams(route.search), route.setParams],
}));
vi.mock('../api', () => ({
  cancelRun: vi.fn().mockResolvedValue({}),
  getTaskRuns: vi.fn(),
  getTemplatesList: vi.fn(),
  getProjects: vi.fn(),
  getActivePrincipal: () => null,
  isAuthenticationRequired: () => false,
}));

const boardRun: TaskRun = {
  id: 7,
  run_id: 'run-board-1',
  feature: 'Онбординг',
  task: 'Идея роста',
  status: 'running',
  phase: 'running',
  current_stage: 'analyst',
  current_function: 'product_owner',
  executor: 'human',
  mode: 'manual',
  allowed_actions: [],
  started_at: '2026-10-12T10:00:00Z',
  stage_entered_at: '2026-10-12T10:05:00Z',
  warnings: 2,
  external_links: ['JIRA-9 https://tracker.example/JIRA-9'],
};

const projectsFixture = [
  { id: 1, name: 'Основной', description: '', archived: false, created_at: '2026-10-15T09:00:00Z', updated_at: '2026-10-15T09:00:00Z' },
  { id: 2, name: 'Платформа', description: 'Задачи платформы', archived: false, created_at: '2026-10-15T09:00:00Z', updated_at: '2026-10-15T09:00:00Z' },
  { id: 3, name: 'Старый', description: '', archived: true, created_at: '2026-10-15T09:00:00Z', updated_at: '2026-10-15T09:00:00Z' },
];

beforeEach(() => {
  ws.onEvent = undefined;
  route.search = '';
  route.setParams.mockClear();
  vi.mocked(getProjects).mockResolvedValue({ projects: projectsFixture });
  vi.mocked(getTemplatesList).mockResolvedValue({
    templates: [{
      valid: true,
      template: 'standard',
      title: 'Стандартный маршрут',
      version: 'abcdef1234567890',
      stages: [
        { id: 'analyst', title: 'Аналитик', function: 'product_owner', result: 'md', executor: 'human' },
        { id: 'architect', title: 'Архитектор', function: 'architect', result: 'md', executor: 'agent', agent: 'architect' },
      ],
    }],
    active_template: 'standard',
    versions: [],
  });
  vi.mocked(getTaskRuns).mockResolvedValue({ runs: [boardRun], total: 1, limit: 50, offset: 0 });
});

afterEach(cleanup);

describe('Tasks board', () => {
  it('раскладывает активные задачи по этапам шаблона и показывает карточку', async () => {
    render(<Tasks />);

    expect((await screen.findAllByText('Аналитик')).length).toBeGreaterThan(0);
    expect(screen.getAllByText('Архитектор').length).toBeGreaterThan(0);
    expect(screen.getByLabelText('Этап: Аналитик')).toBeInTheDocument();
    const card = screen.getByRole('link', { name: 'Задача Идея роста' });
    expect(within(card).getByText('Идея роста')).toBeInTheDocument();
    expect(within(card).getByText('в работе')).toBeInTheDocument();
    expect(screen.getByText('На этапе')).toBeInTheDocument();
    expect(screen.getByText('2 предупреждения')).toBeInTheDocument();
    expect(screen.getByText('Участник')).toBeInTheDocument();
    expect(screen.getByText('Онбординг')).toBeInTheDocument();
    expect(screen.getByLabelText('Внешние ссылки')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Копировать/ })).toBeNull();
  });

  it('фильтрует список запросом к task API и сбрасывает страницу', async () => {
    render(<Tasks />);
    await screen.findByText('Идея роста');

    fireEvent.change(screen.getByLabelText('Состояние'), { target: { value: 'waiting_for_approval' } });

    await waitFor(() => expect(getTaskRuns).toHaveBeenCalledWith(
      expect.objectContaining({ status: 'waiting_for_approval', limit: 50, offset: 0 })));
  });

  it('обновляет данные по событию WebSocket без полной перезагрузки', async () => {
    render(<Tasks />);
    await screen.findByText('Идея роста');
    const calls = vi.mocked(getTaskRuns).mock.calls.length;

    ws.onEvent?.({ type: 'attempt_started' });

    await waitFor(() => expect(vi.mocked(getTaskRuns).mock.calls.length).toBeGreaterThan(calls));
  });

  it('показывает список с пагинацией при большом числе задач', async () => {
    vi.mocked(getTaskRuns).mockResolvedValue({ runs: [boardRun], total: 120, limit: 50, offset: 0 });
    render(<Tasks />);
    await screen.findByText('Идея роста');

    fireEvent.click(screen.getByRole('button', { name: 'Список' }));
    expect(screen.getByLabelText('Список задач')).toBeInTheDocument();
    expect(screen.getByText('1–50 из 120')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Вперёд' }));
    await waitFor(() => expect(getTaskRuns).toHaveBeenCalledWith(
      expect.objectContaining({ offset: 50 })));
    expect(screen.getByText('51–100 из 120')).toBeInTheDocument();
  });

  it('блокированные и ожидающие решения прогоны остаются на своём этапе или в «Ожидает решения»', async () => {
    const blockedNoStage: TaskRun = { ...boardRun, run_id: 'run-blocked', status: 'blocked', current_stage: undefined, task: 'Блокер' };
    const waitingOnStage: TaskRun = { ...boardRun, run_id: 'run-wait', status: 'waiting_for_approval', current_stage: 'architect', task: 'Ждёт решения' };
    const queued: TaskRun = { ...boardRun, run_id: 'run-queued', status: 'queued', current_stage: undefined, task: 'Очередь' };
    vi.mocked(getTaskRuns).mockResolvedValue({ runs: [blockedNoStage, waitingOnStage, queued], total: 3, limit: 50, offset: 0 });
    render(<Tasks />);
    await screen.findByText('Блокер');

    const queue = screen.getByLabelText('Этап: В очереди');
    expect(within(queue).getByText('Очередь')).toBeInTheDocument();
    expect(within(queue).queryByText('Блокер')).toBeNull();
    expect(within(screen.getByLabelText('Этап: Ожидает решения')).getByText('Блокер')).toBeInTheDocument();
    expect(within(screen.getByLabelText('Этап: Архитектор')).getByText('Ждёт решения')).toBeInTheDocument();
    expect(within(queue).queryByText('Этап: —')).toBeNull();
  });

  it('пустые колонки показывают короткий текст «Нет задач»', async () => {
    render(<Tasks />);
    await screen.findByText('Идея роста');

    expect(screen.getByLabelText('Этап: В очереди')).toHaveTextContent('Нет задач');
    expect(screen.getByLabelText('Этап: Архитектор')).toHaveTextContent('Нет задач');
  });

  it('заголовок карточки берёт идею из брифа без строки «# …»', async () => {
    vi.mocked(getTaskRuns).mockResolvedValue({
      runs: [{ ...boardRun, task: '# Бриф\n\nАвторизация форматов бота через Google' }], total: 1, limit: 50, offset: 0,
    });
    render(<Tasks />);

    expect(await screen.findByRole('link', { name: 'Задача Авторизация форматов бота через Google' })).toBeInTheDocument();
    expect(screen.queryByText('# Бриф')).toBeNull();
  });

  it('без идеи заголовок карточки — название шаблона', async () => {
    vi.mocked(getTaskRuns).mockResolvedValue({ runs: [{ ...boardRun, task: undefined }], total: 1, limit: 50, offset: 0 });
    render(<Tasks />);

    expect(await screen.findByRole('link', { name: 'Задача Стандартный маршрут' })).toBeInTheDocument();
  });

  it('отменяет задачу из карточки и показывает ошибку команды', async () => {
    const api = await import('../api');
    const queued: TaskRun = { ...boardRun, run_id: 'run-queued', status: 'queued', current_stage: undefined };
    vi.mocked(getTaskRuns).mockResolvedValue({ runs: [queued], total: 1, limit: 50, offset: 0 });
    render(<Tasks />);
    await screen.findByText('Идея роста');

    vi.mocked(api.cancelRun).mockRejectedValueOnce(new Error('нельзя отменить'));
    fireEvent.click(screen.getByRole('button', { name: 'Отменить задачу' }));
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('нельзя отменить'));
    expect(api.cancelRun).toHaveBeenCalledWith('run-queued');
  });
});

describe('Tasks projects', () => {
  it('фильтрует доску и список по проекту из адреса и передаёт его в API', async () => {
    route.search = 'project=2';
    render(<Tasks />);
    await screen.findByText('Идея роста');

    await waitFor(() => expect(getTaskRuns).toHaveBeenCalledWith(expect.objectContaining({ project_id: '2' })));
    await waitFor(() => expect(screen.getByLabelText('Проект')).toHaveValue('2'));
    expect(screen.getByRole('link', { name: 'Новая задача' })).toHaveAttribute('href', '/tasks/new?project=2');
  });

  it('выбор проекта в фильтре меняет адрес, архивные проекты помечены', async () => {
    render(<Tasks />);
    await screen.findByText('Идея роста');
    expect(await screen.findByRole('option', { name: 'Старый (архив)' })).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Проект'), { target: { value: '2' } });
    expect(route.setParams).toHaveBeenCalledWith({ project: '2' });
    fireEvent.change(screen.getByLabelText('Проект'), { target: { value: '' } });
    expect(route.setParams).toHaveBeenLastCalledWith({});
  });

  it('новый проект без задач показывает понятную пустую страницу с первым шагом', async () => {
    route.search = 'project=2';
    vi.mocked(getTaskRuns).mockResolvedValue({ runs: [], total: 0, limit: 50, offset: 0 });
    render(<Tasks />);

    const empty = await screen.findByLabelText('Пустой проект');
    expect(empty).toHaveTextContent('В проекте «Платформа» пока нет задач');
    expect(empty).toHaveTextContent('Задачи платформы');
    expect(within(empty).getByRole('link', { name: 'Создать первую задачу' })).toHaveAttribute('href', '/tasks/new?project=2');
  });

  it('пустой архивный проект не предлагает создавать задачи', async () => {
    route.search = 'project=3';
    vi.mocked(getTaskRuns).mockResolvedValue({ runs: [], total: 0, limit: 50, offset: 0 });
    render(<Tasks />);

    const empty = await screen.findByLabelText('Пустой проект');
    expect(empty).toHaveTextContent('Проект в архиве');
    expect(within(empty).queryByRole('link', { name: 'Создать первую задачу' })).toBeNull();
  });

  it('без выбранного проекта пустой результат остаётся обычным пустым списком', async () => {
    vi.mocked(getTaskRuns).mockResolvedValue({ runs: [], total: 0, limit: 50, offset: 0 });
    render(<Tasks />);

    expect(await screen.findByLabelText('Задач пока нет')).toHaveTextContent('Новая задача');
    expect(screen.queryByLabelText('Пустой проект')).toBeNull();
  });
});

describe('Tasks polling', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('тикает часами каждые 30 с без запросов и делает recovery-рефетч раз в 2 минуты', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] });
    vi.setSystemTime(new Date('2026-10-12T10:06:00Z'));
    render(<Tasks />);
    await vi.waitFor(() => expect(screen.getByText('Онбординг')).toBeInTheDocument());
    const initialCalls = vi.mocked(getTaskRuns).mock.calls.length;
    expect(screen.getByText('1 мин')).toBeInTheDocument();

    await act(async () => {
      vi.advanceTimersByTime(90_000);
    });
    expect(vi.mocked(getTaskRuns).mock.calls.length).toBe(initialCalls);
    // Часы обновили «сколько на этапе» без сетевого запроса.
    expect(screen.getByText('2 мин')).toBeInTheDocument();

    await act(async () => {
      vi.advanceTimersByTime(30_000);
    });
    expect(vi.mocked(getTaskRuns).mock.calls.length).toBe(initialCalls + 1);
  });
});
