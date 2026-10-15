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

  it('показывает рабочие разделы с понятными подписями и без дубля «Шаблон»', () => {
    renderLayout(false);

    expect(screen.getByRole('link', { name: 'Шаблоны' })).toHaveAttribute('href', '/template');
    expect(screen.getByRole('link', { name: 'Лента' })).toHaveAttribute('href', '/events');
    expect(screen.getByRole('link', { name: 'Наблюдаемость' })).toHaveAttribute('href', '/observability');
    expect(screen.queryByRole('link', { name: 'Шаблон' })).not.toBeInTheDocument();
  });

  it('keeps Team navigation for cloud auth', () => {
    renderLayout(true);

    expect(screen.getByRole('link', { name: 'Команда' })).toBeInTheDocument();
  });

  it('называет главную «Очередь», а доску и список задач — «Задачи»: два пункта, два разных слова', () => {
    renderLayout(false);

    expect(screen.getByRole('link', { name: 'Очередь' })).toHaveAttribute('href', '/');
    expect(screen.getByRole('link', { name: 'Задачи' })).toHaveAttribute('href', '/tasks');
    expect(screen.queryByRole('link', { name: 'Доска' })).not.toBeInTheDocument();
    expect(screen.queryByRole('link', { name: 'Дашборд' })).not.toBeInTheDocument();
  });

  it('показывает «Трекер» только когда трекер подключён', () => {
    const { unmount } = render(<ThemeProvider><MemoryRouter><Layout teamManagementEnabled={false}>x</Layout></MemoryRouter></ThemeProvider>);
    expect(screen.queryByRole('link', { name: 'Трекер' })).not.toBeInTheDocument();
    unmount();
    render(<ThemeProvider><MemoryRouter><Layout teamManagementEnabled={false} trackerEnabled>x</Layout></MemoryRouter></ThemeProvider>);
    expect(screen.getByRole('link', { name: 'Трекер' })).toHaveAttribute('href', '/tracker');
  });
  it('shows the user as avatar and name, keeps roles in a tooltip and the logout as a labelled door', () => {
    renderLayout(false);

    expect(screen.getByRole('button', { name: 'Выйти' })).toHaveAttribute('title', 'Выйти');
    const user = screen.getByText('owner@example.com').closest('[aria-describedby]');
    expect(user).toHaveAttribute('aria-describedby', 'user-roles');
    expect(screen.getByRole('tooltip')).toHaveTextContent('владелец продукта');
    expect(screen.queryByText(/^владелец продукта$/)).toBeNull();
  });
});
