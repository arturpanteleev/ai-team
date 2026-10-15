import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Dashboard } from './Dashboard';
import { MemoryRouter } from '../router';
import { getPipelineRuns } from '../api';

const session = vi.hoisted(() => ({ principal: { actor_id: 'product-1', roles: ['product_owner'] } as { actor_id: string; roles: string[] } | null }));

vi.mock('../hooks/useWebSocket', () => ({ useWebSocket: () => ({ connected: true }) }));
vi.mock('../api', () => ({
  getActivePrincipal: () => session.principal,
  isAuthenticationRequired: () => true,
  getPipelineRuns: vi.fn().mockResolvedValue([]),
  getPreflight: vi.fn().mockResolvedValue({ ready: true, checks: [] }),
  cancelRun: vi.fn(),
  getProjects: vi.fn().mockResolvedValue({ projects: [
    { id: 1, name: 'Основной', description: '', archived: false, created_at: '2026-10-15T09:00:00Z', updated_at: '2026-10-15T09:00:00Z' },
  ] }),
}));

afterEach(() => {
  cleanup();
  vi.mocked(getPipelineRuns).mockResolvedValue([]);
});

function renderDashboard() {
  return render(<MemoryRouter><Dashboard /></MemoryRouter>);
}

describe('Queue page', () => {
  it('называется «Очередь» и ведёт к единственной форме создания задачи', () => {
    session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
    renderDashboard();

    expect(screen.getByRole('heading', { level: 1, name: 'Очередь' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Новая задача/ })).toHaveAttribute('href', '/tasks/new');
    // Второй формы постановки задачи здесь нет: создание живёт на странице «Новая задача».
    expect(screen.queryByLabelText('Какого результата хотите достичь?')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Создать задачу' })).not.toBeInTheDocument();
  });

  it('даёт архитектору создавать задачи так же, как владельцу продукта', () => {
    session.principal = { actor_id: 'arch-1', roles: ['architect'] };
    renderDashboard();

    expect(screen.getByRole('link', { name: /Новая задача/ })).toBeInTheDocument();
  });

  it('называет роли, которые могут создать задачу, участнику без такого права', () => {
    session.principal = { actor_id: 'dev-1', roles: ['developer'] };
    renderDashboard();

    expect(screen.queryByRole('link', { name: /Новая задача/ })).not.toBeInTheDocument();
    expect(screen.getByText(/Создать задачу могут: Владелец продукта, Архитектор/)).toBeInTheDocument();
  });

  it('обрабатывает неизвестную готовность и checks:null, не блокируя создание', async () => {
    session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
    const api = await import('../api');
    vi.mocked(api.getPreflight).mockResolvedValue({ ready: false, unknown: true, checks: null, checked_at: '' });
    renderDashboard();
    expect(await screen.findByText('Статус неизвестен')).toBeInTheDocument();
    expect(screen.getByText(/Задача может подождать worker/)).toBeInTheDocument();
    expect(screen.getAllByRole('link', { name: /Новая задача/ })).toHaveLength(1);
  });

  it('сворачивает фильтры статусов и закрывает панель после выбора', () => {
    session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
    renderDashboard();

    const toggle = screen.getByRole('button', { name: /Статус: Все · Фильтры/ });
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    fireEvent.click(screen.getByRole('button', { name: 'Ждёт решения' }));
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    expect(screen.getByRole('button', { name: /Статус: Ждёт решения/ })).toBeInTheDocument();
  });

  it('пустой список предлагает первый шаг, а не молчит', async () => {
    session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
    renderDashboard();

    expect(await screen.findByText(/Задач пока нет/)).toBeInTheDocument();
    expect(screen.getAllByRole('link', { name: /Новая задача/ })).toHaveLength(1);
  });

  it('показывает ошибку загрузки и позволяет повторить', async () => {
    session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
    vi.mocked(getPipelineRuns).mockRejectedValueOnce(new Error('сервер недоступен'));
    renderDashboard();

    expect(await screen.findByRole('alert')).toHaveTextContent('сервер недоступен');
    fireEvent.click(screen.getByRole('button', { name: 'Повторить загрузку' }));
    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument());
  });

  it('синхронизируется при возврате вкладки', async () => {
    session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
    renderDashboard();

    const previousCalls = vi.mocked(getPipelineRuns).mock.calls.length;
    fireEvent(document, new Event('visibilitychange'));
    await waitFor(() => expect(vi.mocked(getPipelineRuns).mock.calls.length).toBeGreaterThan(previousCalls));
  });
});
