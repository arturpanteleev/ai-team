import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getGlobalEvents } from '../api';
import { useWebSocket } from '../hooks/useWebSocket';
import { MemoryRouter } from '../router';
import type { TaskEvent, WsEvent } from '../types';
import { EventFeed } from './EventFeed';

vi.mock('../api', () => ({ getGlobalEvents: vi.fn() }));
vi.mock('../hooks/useWebSocket', () => ({ useWebSocket: vi.fn() }));

const regularEvent: TaskEvent = {
  version: 1, cursor: 1, run_id: 'run-1', sequence: 1, type: 'run_started',
  timestamp: '2026-10-12T10:00:00Z', data: { feature: 'Первая задача' },
};
const warningEvent: TaskEvent = {
  version: 1, cursor: 2, run_id: 'run-1', sequence: 2, type: 'check_error', stage: 'qa', severity: 'warning',
  timestamp: '2026-10-12T10:01:00Z', data: { reason: 'Проверка не пройдена' },
};

function renderFeed() {
  return render(<MemoryRouter initialEntries={['/events']}><EventFeed /></MemoryRouter>);
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((finish) => { resolve = finish; });
  return { promise, resolve };
}

describe('global event feed', () => {
  beforeEach(() => {
    vi.mocked(useWebSocket).mockReset();
    vi.mocked(getGlobalEvents).mockReset();
    vi.mocked(getGlobalEvents).mockResolvedValue({ events: [regularEvent, warningEvent], next_cursor: 2 });
  });

  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it('shows all events, warning filtering, and stable task links', async () => {
    renderFeed();

    expect(await screen.findByText('Задача создана')).toBeInTheDocument();
    expect(screen.getByText('Ошибка проверки')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Задача: Первая задача' })).toHaveAttribute('href', '/runs/run-1');

    vi.mocked(getGlobalEvents).mockResolvedValue({ events: [warningEvent], next_cursor: 2 });
    fireEvent.click(screen.getByRole('button', { name: 'Предупреждения' }));
    await waitFor(() => expect(getGlobalEvents).toHaveBeenLastCalledWith(0, 100, true));
    expect(screen.queryByText('Задача создана')).not.toBeInTheDocument();
    expect(screen.getByText('Ошибка проверки')).toBeInTheDocument();
    expect(screen.getByText('Проверка не пройдена')).toBeInTheDocument();
  });

  it('shows the newest page first, puts live events on top, and loads older pages below', async () => {
    // 100 newest persisted events, newest-first as the server returns them (cursors 200..101).
    const newestPage: TaskEvent[] = Array.from({ length: 100 }, (_, index) => ({
      ...regularEvent, cursor: 200 - index, sequence: 200 - index, data: { feature: `Задача ${200 - index}` },
    }));
    vi.mocked(getGlobalEvents)
      .mockResolvedValueOnce({ events: newestPage, next_cursor: 101 })
      .mockResolvedValueOnce({ events: [{ ...warningEvent, cursor: 100, sequence: 100 }], next_cursor: 100 });
    renderFeed();
    await waitFor(() => expect(screen.getAllByText('Задача создана')).toHaveLength(100));
    expect(getGlobalEvents).toHaveBeenLastCalledWith(0, 100, false);

    const taskLinks = () => screen.getAllByRole('link').map((link) => link.textContent);
    expect(taskLinks()[0]).toBe('Задача: Задача 200');
    expect(taskLinks()[99]).toBe('Задача: Задача 101');

    const options = vi.mocked(useWebSocket).mock.calls.at(-1)?.[0];
    const liveEvent: WsEvent = {
      version: 1, stream: 'stream-1', cursor: 5000, run_id: 'run-1', sequence: 5000, type: 'stage_skipped', severity: 'warning',
      timestamp: '2026-10-12T10:02:00Z', data: { feature: 'Свежая задача', reason: 'Недоступен' },
    };
    act(() => options?.onEvent?.(liveEvent));
    expect(await screen.findByText('Этап пропущен')).toBeInTheDocument();
    expect(taskLinks()[0]).toBe('Задача: Свежая задача');
    expect(taskLinks()[1]).toBe('Задача: Задача 200');

    // A replayed duplicate of the live event must not render twice.
    act(() => options?.onEvent?.(liveEvent));
    expect(screen.getAllByText('Этап пропущен')).toHaveLength(1);

    fireEvent.click(screen.getByRole('button', { name: 'Показать ещё' }));
    await waitFor(() => expect(getGlobalEvents).toHaveBeenLastCalledWith(101, 100, false));
    expect(await screen.findByText('Ошибка проверки')).toBeInTheDocument();
    expect(taskLinks()).toHaveLength(102);
    expect(taskLinks()[0]).toBe('Задача: Свежая задача');
    expect(taskLinks()[101]).toBe('Задача: run-1');
    expect(screen.queryByRole('button', { name: 'Показать ещё' })).not.toBeInTheDocument();
  });

  it('discards an all-events page that finishes after switching to warnings', async () => {
    const firstPage = Array.from({ length: 100 }, (_, index) => ({
      ...regularEvent, cursor: 200 - index, sequence: 200 - index,
    }));
    const pendingAllEventsPage = deferred<{ events: TaskEvent[]; next_cursor: number }>();
    vi.mocked(getGlobalEvents)
      .mockResolvedValueOnce({ events: firstPage, next_cursor: 101 })
      .mockReturnValueOnce(pendingAllEventsPage.promise)
      .mockResolvedValueOnce({ events: [warningEvent], next_cursor: 2 });
    renderFeed();
    await waitFor(() => expect(screen.getAllByText('Задача создана')).toHaveLength(100));

    fireEvent.click(screen.getByRole('button', { name: 'Показать ещё' }));
    await waitFor(() => expect(getGlobalEvents).toHaveBeenLastCalledWith(101, 100, false));
    fireEvent.click(screen.getByRole('button', { name: 'Предупреждения' }));
    expect(await screen.findByText('Ошибка проверки')).toBeInTheDocument();
    expect(screen.queryByText('Задача создана')).not.toBeInTheDocument();

    await act(async () => {
      pendingAllEventsPage.resolve({ events: [{ ...regularEvent, cursor: 100 }], next_cursor: 100 });
      await pendingAllEventsPage.promise;
    });
    expect(screen.queryByText('Задача создана')).not.toBeInTheDocument();
    expect(screen.getByText('Ошибка проверки')).toBeInTheDocument();
  });
});
