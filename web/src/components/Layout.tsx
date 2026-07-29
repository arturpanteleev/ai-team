import type { ReactNode } from 'react';
import { NavLink } from '../router';
import { getActivePrincipal } from '../api';
import styles from './Layout.module.css';

export function Layout({ children }: { children: ReactNode }) {
  const principal = getActivePrincipal();
  return (
    <div className={styles.layout}>
      <aside className={styles.sidebar}>
        <div className={styles.logo}>ai-team</div>
        <nav className={styles.nav}>
          <NavLink
            to="/"
            end
            className={({ isActive }) =>
              `${styles.navLink} ${isActive ? styles.active : ''}`
            }
          >
            Pipelines
          </NavLink>
        </nav>
        {principal && <small>{principal.actor_id}<br />{principal.roles.join(', ')}</small>}
      </aside>
      <main className={styles.main}>{children}</main>
    </div>
  );
}
