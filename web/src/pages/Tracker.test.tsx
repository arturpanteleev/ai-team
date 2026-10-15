import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MemoryRouter } from '../router';
import { Tracker } from './Tracker';

const session = vi.hoisted(() => ({ principal: { actor_id: 'owner', roles: ['product_owner'] } as { actor_id: string; roles: string[] } }));

vi.mock('../api', () => ({
  getActivePrincipal: () => session.principal,
  isAuthenticationRequired: () => true,
  getTrackerOverview: vi.fn(),
  getTrackerInbound: vi.fn(),
  getTrackerDeliveries: vi.fn(),
  syncTrackerNow: vi.fn(),
}));

const inbound = {
  tracker: 'fake', change_id: 'chg-1', task_id: 5, issue_key: 'DEMO-1', from_status: 'To Do', to_status: 'Blocked', origin: 'external',
  changed_at: '2026-10-15T09:00:00Z', state: 'conflict' as const, action: 'return', to_stage: 'implementation',
  reason: 'возврат не разрешён маршрутом', detected_at: '2026-10-15T09:00:00Z', updated_at: '2026-10-15T09:00:00Z',
};

beforeEach(async () => {
  session.principal = { actor_id: 'owner', roles: ['product_owner'] };
  const api = await import('../api');
  vi.mocked(api.getTrackerOverview).mockReset().mockResolvedValue({
    configured: true, tracker: 'fake', failed_deliveries: 1, link_conflicts: 0,
    inbound: { configured: true, poll_interval_seconds: 60, status: 'ok', delay_seconds: 12, pending_requests: 0, conflicts: 1 },
  });
  vi.mocked(api.getTrackerInbound).mockReset().mockResolvedValue({ requests: [inbound] });
  vi.mocked(api.getTrackerDeliveries).mockReset().mockResolvedValue({ deliveries: [{
    event_id: 3, task_id: 5, tracker: 'fake', issue_key: 'DEMO-1', event_type: 'attempt_started', stage: 'implementation', status: 'failed',
    attempts: 2, last_error: 'timeout', created_at: '2026-10-15T09:00:00Z', updated_at: '2026-10-15T09:00:00Z',
  }] });
  vi.mocked(api.syncTrackerNow).mockReset().mockResolvedValue({ result: {
    tracker: 'fake', trigger: 'manual', changes: 2, applied: 1, pending: 0, conflicts: 1, ignored: 0, duplicates: 0, cursor: 'c',
  } });
});

afterEach(cleanup);

const renderPage = () => render(<MemoryRouter><Tracker /></MemoryRouter>);

describe('Tracker overview', () => {
  it('explains how to connect a tracker when none is configured', async () => {
    const api = await import('../api');
    vi.mocked(api.getTrackerOverview).mockResolvedValue({ configured: false });
    renderPage();
    expect(await screen.findByText(/Трекер задач не подключён/)).toBeInTheDocument();
  });

  it('lists the open inbound requests and failed deliveries with links to their tasks', async () => {
    renderPage();
    expect(await screen.findByRole('heading', { name: /Открытые входящие запросы \(1\)/ })).toBeInTheDocument();
    expect(screen.getByText(/«To Do» → «Blocked»: вернуть на этап «implementation»/)).toBeInTheDocument();
    expect(screen.getByText(/возврат не разрешён маршрутом/)).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: /Не доставлено в трекер \(1\)/ })).toBeInTheDocument();
    expect(screen.getAllByRole('link', { name: 'Карточка DEMO-1' })[0]).toHaveAttribute('href', '/pipelines/5');
    expect(screen.getByText('timeout')).toBeInTheDocument();
  });

  it('runs a manual sync and reports what it did', async () => {
    const api = await import('../api');
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Синхронизировать сейчас' }));
    await waitFor(() => expect(api.syncTrackerNow).toHaveBeenCalled());
    expect(await screen.findByText(/Изменений: 2, выполнено: 1/)).toBeInTheDocument();
  });

  it('shows the sync error from the server and keeps the page usable', async () => {
    const api = await import('../api');
    vi.mocked(api.syncTrackerNow).mockRejectedValue(new Error('the tracker could not be read; nothing was lost'));
    renderPage();
    fireEvent.click(await screen.findByRole('button', { name: 'Синхронизировать сейчас' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('nothing was lost');
    expect(screen.getByRole('button', { name: 'Синхронизировать сейчас' })).toBeEnabled();
  });

  it('does not offer sync to a role without tracker:link', async () => {
    session.principal = { actor_id: 'dev', roles: ['developer'] };
    renderPage();
    expect(await screen.findByText(/Запускать синхронизацию могут: Владелец продукта, Архитектор/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Синхронизировать сейчас' })).not.toBeInTheDocument();
  });

  it('shows an API error with a retry button', async () => {
    const api = await import('../api');
    vi.mocked(api.getTrackerOverview).mockRejectedValueOnce(new Error('сервер недоступен'));
    renderPage();
    expect(await screen.findByRole('alert')).toHaveTextContent('сервер недоступен');
    fireEvent.click(screen.getByRole('button', { name: 'Повторить загрузку' }));
    expect(await screen.findByRole('heading', { name: /Открытые входящие запросы/ })).toBeInTheDocument();
  });
});
