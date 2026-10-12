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

  it('appends live events and loads the next cursor page without duplicates', async () => {
    const firstPage = [regularEvent, warningEvent, ...Array.from({ length: 98 }, (_, index) => ({
      ...regularEvent, cursor: index + 3, sequence: index + 3,
    }))];
    vi.mocked(getGlobalEvents)
      .mockResolvedValueOnce({ events: firstPage, next_cursor: 100 })
      .mockResolvedValueOnce({ events: [{ ...warningEvent, cursor: 101, sequence: 101 }], next_cursor: 101 });
    renderFeed();
    expect(await screen.findByText('Ошибка проверки')).toBeInTheDocument();

    const options = vi.mocked(useWebSocket).mock.calls.at(-1)?.[0];
    const liveEvent: WsEvent = {
      version: 1, stream: 'stream-1', cursor: 5, run_id: 'run-1', sequence: 5, type: 'stage_skipped', severity: 'warning',
      timestamp: '2026-10-12T10:02:00Z', data: { stage: 'review', reason: 'Недоступен' },
    };
    act(() => options?.onEvent?.(liveEvent));
    expect(await screen.findByText('Этап пропущен')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Показать ещё' }));
    await waitFor(() => expect(getGlobalEvents).toHaveBeenLastCalledWith(100, 100, false));
    expect(screen.getAllByText('Ошибка проверки')).toHaveLength(2);
    expect(screen.getByText('Этап пропущен')).toBeInTheDocument();
  });
});
