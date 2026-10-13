import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Dashboard } from './Dashboard';
import { MemoryRouter } from '../router';
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

// Форма создания открывается кнопкой «Создать задачу» в правой колонке.
function openCreateForm() {
  fireEvent.click(screen.getByRole('button', { name: 'Создать задачу' }));
}

describe('Dashboard business intake', () => {
  it('дает Product Owner кнопку создания, которая открывает форму постановки бизнес-цели', () => {
    session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
    render(<Dashboard />);

    expect(screen.queryByLabelText('Какого результата хотите достичь?')).not.toBeInTheDocument();
    openCreateForm();
    expect(screen.getByLabelText('Какого результата хотите достичь?')).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/Аналитик уточнит недостающие детали/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Создать задачу' })).toBeInTheDocument();
  });

  it('не отправляет инициативу с идентификатором не в формате slug', async () => {
    session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
    render(<Dashboard />);

    openCreateForm();
    fireEvent.change(screen.getByLabelText('Название задачи'), { target: { value: 'Рост продаж' } });
    fireEvent.change(screen.getByLabelText('Короткий идентификатор'), { target: { value: 'рост продаж' } });
    fireEvent.change(screen.getByLabelText('Какого результата хотите достичь?'), { target: { value: 'Цель' } });
    fireEvent.click(screen.getByRole('button', { name: 'Создать задачу' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('Первый символ должен быть латинской буквой или цифрой, а не «р».');
    expect(startRun).not.toHaveBeenCalled();
  });

  it('направляет участника без роли Product Owner к владельцу бизнес-требований', () => {
    session.principal = { actor_id: 'dev-1', roles: ['developer'] };
    render(<Dashboard />);

    expect(screen.getByText('Создать задачу может Product Owner.')).toBeInTheDocument();
    expect(screen.queryByLabelText('Какого результата хотите достичь?')).not.toBeInTheDocument();
  });

  it('обрабатывает неизвестную готовность и checks:null, оставляя enqueue доступным', async () => {
    session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
    const api = await import('../api');
    vi.mocked(api.getPreflight).mockResolvedValue({ ready: false, unknown: true, checks: null, checked_at: '' });
    render(<Dashboard />);
    expect(await screen.findByText('Статус неизвестен')).toBeInTheDocument();
    expect(screen.getByText(/Задача может подождать worker/)).toBeInTheDocument();
    openCreateForm();
    expect(screen.getByRole('button', { name: 'Создать задачу' })).toBeEnabled();
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
    render(<MemoryRouter><Dashboard /></MemoryRouter>);

    openCreateForm();
    fireEvent.change(screen.getByLabelText('Название задачи'), { target: { value: 'Mobile flow' } });
    fireEvent.change(screen.getByLabelText('Какого результата хотите достичь?'), { target: { value: 'Согласовать требования с телефона' } });
    fireEvent.click(screen.getByRole('button', { name: 'Создать задачу' }));
    await waitFor(() => expect(startRun).toHaveBeenCalledWith('mobile-flow', 'Согласовать требования с телефона'));
    await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('Mobile flow'));
    expect(screen.getByRole('link', { name: 'Открыть' })).toHaveAttribute('href', '/tasks/run-mobile-created');

    const previousCalls = vi.mocked(getPipelineRuns).mock.calls.length;
    fireEvent(document, new Event('visibilitychange'));
    await waitFor(() => expect(vi.mocked(getPipelineRuns).mock.calls.length).toBeGreaterThan(previousCalls));
  });
});
