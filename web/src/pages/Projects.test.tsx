import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Projects } from './Projects';
import { MemoryRouter } from '../router';
import { createProject, getProjects, setProjectArchived } from '../api';

const session = vi.hoisted(() => ({
  principal: { actor_id: 'product-1', roles: ['product_owner'] } as { actor_id: string; roles: string[] } | null,
  authenticationRequired: true,
}));

vi.mock('../api', () => ({
  createProject: vi.fn(),
  getProjects: vi.fn(),
  setProjectArchived: vi.fn(),
  getActivePrincipal: () => session.principal,
  isAuthenticationRequired: () => session.authenticationRequired,
}));

const stamp = '2026-10-15T09:00:00Z';
const mainProject = { id: 1, name: 'Основной', description: 'Старые задачи', archived: false, created_at: stamp, updated_at: stamp };
const oldProject = { id: 3, name: 'Старый', description: '', archived: true, created_at: stamp, updated_at: stamp };

beforeEach(() => {
  session.principal = { actor_id: 'product-1', roles: ['product_owner'] };
  session.authenticationRequired = true;
  vi.mocked(getProjects).mockReset().mockResolvedValue({ projects: [mainProject, oldProject] });
  vi.mocked(createProject).mockReset();
  vi.mocked(setProjectArchived).mockReset();
});

afterEach(cleanup);

function renderPage() {
  return render(<MemoryRouter><Projects /></MemoryRouter>);
}

describe('Projects page', () => {
  it('показывает активные и архивные проекты со ссылкой на доску проекта', async () => {
    renderPage();

    const active = await screen.findByRole('region', { name: 'Активные' });
    expect(within(active).getByText('Основной')).toBeInTheDocument();
    expect(within(active).getByRole('link', { name: 'Открыть доску' })).toHaveAttribute('href', '/tasks?project=1');
    const archive = screen.getByRole('region', { name: 'Архив (1)' });
    expect(within(archive).getByText('Старый')).toBeInTheDocument();
    expect(within(archive).getByRole('button', { name: 'Вернуть из архива: Старый' })).toBeInTheDocument();
  });

  it('создаёт проект и перечитывает список', async () => {
    vi.mocked(createProject).mockResolvedValue({ id: 2, name: 'Платформа', description: 'О платформе', archived: false, created_at: stamp, updated_at: stamp });
    renderPage();
    await screen.findByText('Основной');

    fireEvent.change(screen.getByLabelText('Название'), { target: { value: '  Платформа ' } });
    fireEvent.change(screen.getByLabelText('Описание'), { target: { value: 'О платформе' } });
    fireEvent.click(screen.getByRole('button', { name: 'Создать проект' }));

    await waitFor(() => expect(createProject).toHaveBeenCalledWith('Платформа', 'О платформе'));
    expect(await screen.findByText('Проект «Платформа» создан.')).toBeInTheDocument();
    await waitFor(() => expect(getProjects).toHaveBeenCalledTimes(2));
  });

  it('показывает отказ сервера при создании', async () => {
    vi.mocked(createProject).mockRejectedValue(new Error('project name is already in use'));
    renderPage();
    await screen.findByText('Основной');

    fireEvent.change(screen.getByLabelText('Название'), { target: { value: 'Основной' } });
    fireEvent.click(screen.getByRole('button', { name: 'Создать проект' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('project name is already in use');
  });

  it('архивирует проект', async () => {
    vi.mocked(setProjectArchived).mockResolvedValue({ ...mainProject, archived: true });
    renderPage();
    await screen.findByText('Основной');

    fireEvent.click(screen.getByRole('button', { name: 'Архивировать: Основной' }));
    await waitFor(() => expect(setProjectArchived).toHaveBeenCalledWith(1, true));
  });

  it('участнику без права управления не показывает форму и кнопки архивации', async () => {
    session.principal = { actor_id: 'dev-1', roles: ['developer'] };
    renderPage();
    await screen.findByText('Основной');

    expect(screen.queryByRole('button', { name: 'Создать проект' })).toBeNull();
    expect(screen.queryByRole('button', { name: /Архивировать/ })).toBeNull();
    expect(screen.getAllByRole('link', { name: 'Открыть доску' })).toHaveLength(2);
  });
});
