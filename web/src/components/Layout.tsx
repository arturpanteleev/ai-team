import type { ReactNode } from 'react';
import { NavLink } from '../router';
import { closeSession, getActivePrincipal } from '../api';
import { useTheme } from '../theme';
import { Bubble } from './Bubble';
import type { BubbleTone } from './Bubble';
import styles from './Layout.module.css';

type IconName = 'tasks' | 'board' | 'template' | 'feed' | 'observability' | 'team';

const navTone: Record<IconName, BubbleTone> = {
  tasks: 'yellow', board: 'mint', template: 'lilac', feed: 'coral', observability: 'blue', team: 'yellow',
};

// Иконки намеренно простые: контур 1.8px, наследуют цвет текста (внутри пузыря).
function NavIcon({ name }: { name: IconName }) {
  const paths: Record<IconName, ReactNode> = {
    tasks: <><rect x="4" y="4" width="16" height="16" rx="4" /><path d="M8 12.5l2.5 2.5L16 9.5" /></>,
    board: <><rect x="3.5" y="4" width="5" height="16" rx="1.5" /><rect x="9.5" y="4" width="5" height="10" rx="1.5" /><rect x="15.5" y="4" width="5" height="13" rx="1.5" /></>,
    template: <><path d="M7 3.5h7l4 4V20a1 1 0 0 1-1 1H7a1 1 0 0 1-1-1V4.5a1 1 0 0 1 1-1z" /><path d="M9 12h6M9 15.5h4" /></>,
    feed: <><path d="M4 7h16M4 12h11M4 17h7" /><circle cx="18" cy="16.5" r="2.5" /></>,
    observability: <><path d="M3.5 17l5-5 3.5 3.5 4.5-6 4 4" /><path d="M3.5 20.5h17" /></>,
    team: <><circle cx="9" cy="9" r="3.2" /><path d="M3.5 19.5c.8-3 2.9-4.6 5.5-4.6s4.7 1.6 5.5 4.6" /><path d="M16 6.2a3 3 0 0 1 0 5.6M18.2 14.9c1.3.7 2 2.2 2.3 4.6" /></>,
  };
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
      {paths[name]}
    </svg>
  );
}

function NavItem({ name, to, end, children }: { name: IconName; to: string; end?: boolean; children: ReactNode }) {
  return (
    <NavLink to={to} end={end} className={navClass}>
      <Bubble tone={navTone[name]} size="sm" className={styles.navBubble}><NavIcon name={name} /></Bubble>
      <span>{children}</span>
    </NavLink>
  );
}

function navClass({ isActive }: { isActive: boolean }) {
  return `${styles.navLink} ${isActive ? styles.active : ''}`;
}

// Значок двери со стрелкой: подпись «Выйти» задаётся aria-label и title.
function DoorIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
      <path d="M10 4H6a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h4" />
      <path d="M15 8l4 4-4 4" />
      <path d="M19 12H9" />
    </svg>
  );
}

export function Layout({ children, teamManagementEnabled }: { children: ReactNode; teamManagementEnabled: boolean }) {
  const principal = getActivePrincipal();
  const { theme, toggleTheme } = useTheme();
  const initial = principal?.actor_id.trim().charAt(0).toUpperCase() || '?';
  const roles = principal ? principal.roles.map((role) => roleNames[role] ?? role).join(', ') : '';
  return (
    <div className={styles.layout}>
      <aside className={styles.sidebar}>
        <div className={styles.logo}>
          <span className={styles.logoMark} aria-hidden="true" />
          ai-team
        </div>
        <nav className={styles.nav} aria-label="Разделы">
          <NavItem to="/" end name="tasks">Задачи</NavItem>
          <NavItem to="/tasks" name="board">Доска</NavItem>
          <NavItem to="/template" name="template">Шаблоны</NavItem>
          <NavItem to="/events" name="feed">Лента</NavItem>
          <NavItem to="/observability" name="observability">Наблюдаемость</NavItem>
          {teamManagementEnabled && principal?.roles.includes('product_owner') && (
            <NavItem to="/team" name="team">Команда</NavItem>
          )}
        </nav>
        <div className={styles.footer}>
          <button className={styles.themeToggle} type="button" onClick={toggleTheme} aria-label="Переключить тему">
            {theme === 'light' ? 'Тёмная тема' : 'Светлая тема'}
          </button>
          {principal && (
            <div className={styles.user} tabIndex={0} aria-describedby="user-roles">
              <span className={styles.avatar} aria-hidden="true">{initial}</span>
              <span className={styles.userName}>{principal.actor_id}</span>
              <span role="tooltip" id="user-roles" className={styles.tooltip}>Роли: {roles || 'не назначены'}</span>
              <button className={styles.logout} type="button" aria-label="Выйти" title="Выйти"
                onClick={() => void closeSession()}>
                <DoorIcon />
              </button>
            </div>
          )}
        </div>
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
