import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Dashboard } from './Dashboard';
import { getPipelineRuns, startRun } from '../api';

const session = vi.hoisted(() => ({ principal: { actor_id: 'product-1', roles: ['product_owner'] } }));

vi.mock('../hooks/useWebSocket', () => ({ useWebSocket: () => ({ connected: true }) }));
vi.mock('../api', () => ({
  getActivePrincipal: () => session.principal,
  getPipelineRuns: vi.fn().mockResolvedValue([]),
  getPreflight: vi.fn().mockResolvedValue({ ready: true, checks: [] }),
  startRun: vi.fn(),
}));

afterEach(cleanup);

describe('Dashboard business intake', () => {
  it('дает Product Owner форму для постановки бизнес-цели с подсказкой про уточнение', () => {
    session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
    render(<Dashboard />);

    expect(screen.getByLabelText('Какого результата хотите достичь?')).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/Аналитик уточнит недостающие детали/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Создать инициативу и передать аналитику' })).toBeInTheDocument();
  });

  it('направляет участника без роли Product Owner к владельцу бизнес-требований', () => {
    session.principal = { actor_id: 'dev-1', roles: ['developer'] };
    render(<Dashboard />);

    expect(screen.getByText('Создать инициативу может Product Owner.')).toBeInTheDocument();
    expect(screen.queryByLabelText('Какого результата хотите достичь?')).not.toBeInTheDocument();
  });

  it('обрабатывает неизвестную готовность и checks:null, оставляя enqueue доступным', async () => {
    session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
    const api = await import('../api');
    vi.mocked(api.getPreflight).mockResolvedValue({ ready: false, unknown: true, checks: null, checked_at: '' });
    render(<Dashboard />);
    expect(await screen.findByText(/неизвестна · задача может ждать worker/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Создать инициативу и передать аналитику' })).toBeEnabled();
  });

  it('сворачивает фильтры статусов и закрывает панель после выбора', () => {
    session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
    render(<Dashboard />);

    const toggle = screen.getByRole('button', { name: /Статус: Все · Фильтры/ });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    fireEvent.click(screen.getByRole('button', { name: 'Ждёт решения' }));
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    expect(screen.getByRole('button', { name: /Статус: Ждёт решения/ })).toBeInTheDocument();
  });

  it('после создания инициативы обновляет список и синхронизируется при возврате вкладки', async () => {
    session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
    vi.mocked(startRun).mockResolvedValueOnce({ run_id: 'run-mobile-created' });
    render(<Dashboard />);

    fireEvent.change(screen.getByLabelText('Название инициативы'), { target: { value: 'Mobile flow' } });
    fireEvent.change(screen.getByLabelText('Какого результата хотите достичь?'), { target: { value: 'Согласовать требования с телефона' } });
    fireEvent.click(screen.getByRole('button', { name: 'Создать инициативу и передать аналитику' }));
    await waitFor(() => expect(startRun).toHaveBeenCalledWith('Mobile flow', 'Согласовать требования с телефона'));
    await waitFor(() => expect(screen.getByText('Run принят: run-mobile-created')).toBeInTheDocument());

    const previousCalls = vi.mocked(getPipelineRuns).mock.calls.length;
    fireEvent(document, new Event('visibilitychange'));
    await waitFor(() => expect(vi.mocked(getPipelineRuns).mock.calls.length).toBeGreaterThan(previousCalls));
  });
});
