import type { ReactNode } from 'react';
import { NavLink } from '../router';
import { getActivePrincipal } from '../api';
import { useTheme } from '../theme';
import styles from './Layout.module.css';

export function Layout({ children, teamManagementEnabled }: { children: ReactNode; teamManagementEnabled: boolean }) {
  const principal = getActivePrincipal();
  const { theme, toggleTheme } = useTheme();
  return (
    <div className={styles.layout}>
      <aside className={styles.sidebar}>
        <div className={styles.logo}>ai-team</div>
        <button className={styles.themeToggle} type="button" onClick={toggleTheme} aria-label="Переключить тему">
          {theme === 'light' ? 'Тёмная тема' : 'Светлая тема'}
        </button>
        <nav className={styles.nav}>
          <NavLink
            to="/"
            end
            className={({ isActive }) =>
              `${styles.navLink} ${isActive ? styles.active : ''}`
            }
          >
            Задачи
          </NavLink>
          {teamManagementEnabled && principal?.roles.includes('product_owner') && <NavLink to="/team" className={({ isActive }) => `${styles.navLink} ${isActive ? styles.active : ''}`}>Команда</NavLink>}
        </nav>
        {principal && <small>{principal.actor_id}<br />{principal.roles.map((role) => roleNames[role] ?? role).join(', ')}</small>}
      </aside>
      <main className={styles.main}>{children}</main>
    </div>
  );
}

const roleNames: Record<string, string> = {
  product_owner: 'владелец продукта',
  architect: 'архитектор',
  developer: 'разработчик',
  reviewer: 'ревьюер',
  qa: 'тестировщик',
  release_manager: 'менеджер выпуска',
};
