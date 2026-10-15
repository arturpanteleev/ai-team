import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { TrackerActions, TrackerDetails, TrackerSummary } from './TrackerPanel';
import type { RunTrackerController } from '../trackerState';
import type { RunTracker } from '../types/tracker';

vi.mock('../api', () => ({
  dismissTrackerInbound: vi.fn().mockResolvedValue({}),
  removeRunTrackerLink: vi.fn().mockResolvedValue({}),
  retryRunTrackerDeliveries: vi.fn().mockResolvedValue({}),
  setRunTrackerLink: vi.fn().mockResolvedValue({}),
  syncTrackerNow: vi.fn().mockResolvedValue({}),
}));

afterEach(cleanup);

const linked: RunTracker = {
  configured: true, tracker: 'fake', sync_status: 'ok',
  link: { task_id: 1, tracker: 'fake', issue_key: 'DEMO-1', linked_by: 'owner', linked_at: '2026-10-15T09:00:00Z' },
  conflicts: [], deliveries: [], inbound: [],
};

function controller(data: RunTracker | null, extra: Partial<RunTrackerController> = {}): RunTrackerController {
  return {
    data, overview: { configured: true, inbound: { configured: true, poll_interval_seconds: 60, status: 'ok' } },
    error: '', status: '', busy: false, canLink: true, reload: vi.fn().mockResolvedValue(undefined),
    act: vi.fn(async (_label: string, action: () => Promise<unknown>) => { await action(); return true; }),
    ...extra,
  };
}

describe('Tracker panel', () => {
  it('renders nothing when no tracker is configured', () => {
    const { container } = render(<><TrackerSummary tracker={controller({ configured: false, sync_status: 'not_configured' })} />
      <TrackerActions runId="run-1" tracker={controller({ configured: false, sync_status: 'not_configured' })} /></>);
    expect(container).toBeEmptyDOMElement();
  });

  it('shows the linked card and the delivery state in the status column', () => {
    render(<TrackerSummary tracker={controller({ ...linked, sync_status: 'failed',
      deliveries: [{ event_id: 7, task_id: 1, tracker: 'fake', issue_key: 'DEMO-1', event_type: 'attempt_started', status: 'failed', attempts: 2,
        last_error: 'timeout', created_at: '2026-10-15T09:00:00Z', updated_at: '2026-10-15T09:01:00Z' }] })} />);
    const summary = screen.getByTestId('tracker-summary');
    expect(summary).toHaveTextContent('DEMO-1');
    expect(summary).toHaveTextContent('Ошибка отправки в трекер');
    expect(summary).toHaveTextContent('Не доставлено: 1');
  });

  it('links a task to a card by the issue key', async () => {
    const api = await import('../api');
    const tracker = controller({ configured: true, tracker: 'fake', sync_status: 'not_linked', link: null });
    render(<TrackerActions runId="run-1" tracker={tracker} />);

    expect(screen.queryByRole('button', { name: 'Снять связь' })).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Ключ карточки'), { target: { value: ' DEMO-2 ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Связать с карточкой' }));
    await waitFor(() => expect(api.setRunTrackerLink).toHaveBeenCalledWith('run-1', 'DEMO-2'));
  });

  it('offers unlink, retry of failed deliveries and sync now for a linked task', async () => {
    const api = await import('../api');
    const failed = { event_id: 7, task_id: 1, tracker: 'fake', issue_key: 'DEMO-1', event_type: 'attempt_started', status: 'failed', attempts: 1,
      created_at: '2026-10-15T09:00:00Z', updated_at: '2026-10-15T09:01:00Z' };
    render(<TrackerActions runId="run-1" tracker={controller({ ...linked, sync_status: 'failed', deliveries: [failed] })} />);

    fireEvent.click(screen.getByRole('button', { name: 'Повторить отправку в трекер (1)' }));
    await waitFor(() => expect(api.retryRunTrackerDeliveries).toHaveBeenCalledWith('run-1'));
    fireEvent.click(screen.getByRole('button', { name: 'Синхронизировать сейчас' }));
    await waitFor(() => expect(api.syncTrackerNow).toHaveBeenCalled());
    fireEvent.click(screen.getByRole('button', { name: 'Снять связь' }));
    await waitFor(() => expect(api.removeRunTrackerLink).toHaveBeenCalledWith('run-1'));
  });

  it('hides every write action from a role without tracker:link and names who may', () => {
    render(<TrackerActions runId="run-1" tracker={controller({ ...linked }, { canLink: false })} />);

    expect(screen.queryByRole('button')).not.toBeInTheDocument();
    expect(screen.getByText(/Связь с карточкой меняют: Владелец продукта, Архитектор/)).toBeInTheDocument();
  });

  it('shows an open inbound request with the reason and closes it only with a reason', async () => {
    const api = await import('../api');
    const request = {
      tracker: 'fake', change_id: 'chg-1', task_id: 1, issue_key: 'DEMO-1', from_status: 'In Progress', to_status: 'Done', origin: 'external',
      author: 'jira-user', changed_at: '2026-10-15T09:00:00Z', state: 'conflict' as const, action: 'advance',
      reason: 'этап ждёт подтверждения человека', suggested_action: 'принять этап', allowed_actions: ['confirm'],
      detected_at: '2026-10-15T09:00:00Z', updated_at: '2026-10-15T09:00:00Z',
    };
    render(<TrackerDetails runId="run-1" tracker={controller({ ...linked, sync_status: 'inbound_pending', inbound: [request] })} />);

    expect(screen.getByText(/«In Progress» → «Done»: продолжить этап/)).toBeInTheDocument();
    expect(screen.getByText(/этап ждёт подтверждения человека/)).toBeInTheDocument();
    expect(screen.getByText(/Допустимо сейчас: принять этап/)).toBeInTheDocument();
    expect(screen.getByText(/Контроллер разрешает: Принять и перейти/)).toBeInTheDocument();
    const dismiss = screen.getByRole('button', { name: 'Закрыть запрос без действий' });
    expect(dismiss).toBeDisabled();
    fireEvent.change(screen.getByLabelText('Причина закрытия'), { target: { value: 'решим вручную' } });
    fireEvent.click(dismiss);
    await waitFor(() => expect(api.dismissTrackerInbound).toHaveBeenCalledWith('run-1', 'chg-1', 'решим вручную'));
  });

  it('shows link conflicts and the poll state, and explains when inbound sync is off', () => {
    const data: RunTracker = { configured: true, tracker: 'fake', sync_status: 'conflict', link: null,
      conflicts: [{ task_id: 1, issue_key: 'OLD-1', reason: 'в задаче было несколько ссылок', detected_at: '2026-10-15T09:00:00Z' }],
      deliveries: [], inbound: [] };
    render(<TrackerDetails runId="run-1" tracker={controller(data, { overview: { configured: true, inbound: { configured: false } } })} />);

    expect(screen.getByRole('region', { name: 'Конфликты связи' })).toHaveTextContent('в задаче было несколько ссылок');
    expect(screen.getByText(/Входящая синхронизация не настроена/)).toBeInTheDocument();
    expect(screen.getByText('Запросов от трекера по этой задаче нет.')).toBeInTheDocument();
  });

  it('shows the API error instead of a blank panel', () => {
    render(<TrackerDetails runId="run-1" tracker={controller(null, { error: 'tracker state is unavailable' })} />);
    expect(screen.getByRole('alert')).toHaveTextContent('tracker state is unavailable');
  });
});
