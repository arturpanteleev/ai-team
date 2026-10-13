import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Tasks } from './Tasks';
import { getTaskRuns, getTemplatesList } from '../api';
import type { TaskRun } from '../types';

const ws = vi.hoisted(() => ({ onEvent: undefined as ((event: unknown) => void) | undefined }));

vi.mock('../hooks/useWebSocket', () => ({
  useWebSocket: (options: { onEvent?: (event: unknown) => void }) => {
    ws.onEvent = options.onEvent;
  },
}));
vi.mock('../router', () => ({
  Link: ({ to, children }: { to: string; children: ReactNode }) => <a href={to}>{children}</a>,
  useNavigate: () => () => {},
}));
vi.mock('../api', () => ({
  cancelRun: vi.fn().mockResolvedValue({}),
  getTaskRuns: vi.fn(),
  getTemplatesList: vi.fn(),
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

beforeEach(() => {
  ws.onEvent = undefined;
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
    expect(screen.getByRole('link', { name: 'Задача Онбординг' })).toBeInTheDocument();
    expect(screen.getByText('Идея роста')).toBeInTheDocument();
    expect(screen.getByText('На этапе')).toBeInTheDocument();
    expect(screen.getByTitle('Предупреждений: 2')).toBeInTheDocument();
    expect(screen.getByText('Участник')).toBeInTheDocument();
    expect(screen.getByLabelText('Внешние ссылки')).toBeInTheDocument();
  });

  it('фильтрует список запросом к task API и сбрасывает страницу', async () => {
    render(<Tasks />);
    await screen.findByText('Онбординг');

    fireEvent.change(screen.getByLabelText('Состояние'), { target: { value: 'waiting_for_approval' } });

    await waitFor(() => expect(getTaskRuns).toHaveBeenCalledWith(
      expect.objectContaining({ status: 'waiting_for_approval', limit: 50, offset: 0 })));
  });

  it('обновляет данные по событию WebSocket без полной перезагрузки', async () => {
    render(<Tasks />);
    await screen.findByText('Онбординг');
    const calls = vi.mocked(getTaskRuns).mock.calls.length;

    ws.onEvent?.({ type: 'attempt_started' });

    await waitFor(() => expect(vi.mocked(getTaskRuns).mock.calls.length).toBeGreaterThan(calls));
  });

  it('показывает список с пагинацией при большом числе задач', async () => {
    vi.mocked(getTaskRuns).mockResolvedValue({ runs: [boardRun], total: 120, limit: 50, offset: 0 });
    render(<Tasks />);
    await screen.findByText('Онбординг');

    fireEvent.click(screen.getByRole('button', { name: 'Список' }));
    expect(screen.getByLabelText('Список задач')).toBeInTheDocument();
    expect(screen.getByText('1–50 из 120')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Вперёд' }));
    await waitFor(() => expect(getTaskRuns).toHaveBeenCalledWith(
      expect.objectContaining({ offset: 50 })));
    expect(screen.getByText('51–100 из 120')).toBeInTheDocument();
  });

  it('отменяет задачу из карточки и показывает ошибку команды', async () => {
    const api = await import('../api');
    const queued: TaskRun = { ...boardRun, run_id: 'run-queued', status: 'queued', current_stage: undefined };
    vi.mocked(getTaskRuns).mockResolvedValue({ runs: [queued], total: 1, limit: 50, offset: 0 });
    render(<Tasks />);
    await screen.findByText('Онбординг');

    vi.mocked(api.cancelRun).mockRejectedValueOnce(new Error('нельзя отменить'));
    fireEvent.click(screen.getByRole('button', { name: 'Отменить' }));
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('нельзя отменить'));
    expect(api.cancelRun).toHaveBeenCalledWith('run-queued');
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
