import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Dashboard } from './Dashboard';

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
});
