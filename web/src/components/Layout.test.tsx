import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Layout } from './Layout';
import { MemoryRouter } from '../router';
import { ThemeProvider } from '../theme';

vi.mock('../api', () => ({
  getActivePrincipal: () => ({ actor_id: 'owner@example.com', roles: ['product_owner'] }),
}));

afterEach(cleanup);

function renderLayout(teamManagementEnabled: boolean) {
  return render(
    <ThemeProvider>
      <MemoryRouter>
        <Layout teamManagementEnabled={teamManagementEnabled}>content</Layout>
      </MemoryRouter>
    </ThemeProvider>,
  );
}

describe('Layout team navigation', () => {
  it('hides Team in local mode', () => {
    renderLayout(false);

    expect(screen.queryByRole('link', { name: 'Команда' })).not.toBeInTheDocument();
  });

  it('оставляет в меню только рабочие разделы: без ленты, наблюдаемости и отдельного «Шаблон»', () => {
    renderLayout(false);

    expect(screen.getByRole('link', { name: 'Шаблоны' })).toHaveAttribute('href', '/template');
    expect(screen.queryByRole('link', { name: 'Лента' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Наблюдаемость' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Шаблон' })).not.toBeInTheDocument();
  });

  it('keeps Team navigation for cloud auth', () => {
    renderLayout(true);

    expect(screen.getByRole('link', { name: 'Команда' })).toBeInTheDocument();
  });

  it('ведёт на дашборд и на доску задач разными пунктами без дубля «Задачи»', () => {
    renderLayout(false);

    expect(screen.getByRole('link', { name: 'Дашборд' })).toHaveAttribute('href', '/');
    expect(screen.getByRole('link', { name: 'Доска' })).toHaveAttribute('href', '/tasks');
    expect(screen.queryByRole('link', { name: 'Задачи' })).not.toBeInTheDocument();
  });
});
