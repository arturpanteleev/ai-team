import { useState, useEffect, useCallback } from 'react';
import type { PipelineRun, PipelineStatus, PreflightReport } from '../types';
import { cancelRun, getPipelineRuns, getPreflight } from '../api';
import { actionVerbs } from '../actionLabels';
import { mayPerform, permissionRoles } from '../permissions';
import { getActivePrincipal } from '../api';
import { useWebSocket } from '../hooks/useWebSocket';
import { PipelineCard } from '../components/PipelineCard';
import { Link } from '../router';
import { statusLabels } from '../statusLabels';
import { usePageTitle } from '../hooks/usePageTitle';
import { projectLabel, useProjects } from '../hooks/useProjects';
import styles from './Dashboard.module.css';

type Filter = 'all' | PipelineStatus;

const filters: Filter[] = [
  'all',
  'queued',
  'running',
  'waiting_for_approval',
  'completed',
  'completed_with_warnings',
  'failed',
  'blocked',
  'stopped',
  'canceled',
  'interrupted',
];

const filterLabels: Record<Filter, string> = {
  all: 'Все',
  queued: 'В очереди',
  running: 'Выполняется',
  waiting_for_approval: 'Ждёт решения',
  completed: 'Завершена',
  completed_with_warnings: 'Есть замечания',
  failed: 'Ошибка',
  blocked: 'Заблокирована',
  stopped: 'Остановлена',
  canceled: 'Отменена',
  interrupted: 'Прервана',
};

const activeStatuses: PipelineStatus[] = ['queued', 'running', 'waiting_for_approval'];

// «Очередь»: что сейчас в работе и что ждёт решения. Доска по этапам и список всех
// задач — на странице «Задачи». Задача создаётся одним путём: страница «Новая задача».
export function Dashboard() {
  usePageTitle('Очередь');
  const canStart = mayPerform(getActivePrincipal(), 'run:start');
  const [runs, setRuns] = useState<PipelineRun[]>([]);
  const { projects } = useProjects();
  const [projectFilter, setProjectFilter] = useState('');
  const [filter, setFilter] = useState<Filter>('all');
  const [query, setQuery] = useState('');
  const [filtersExpanded, setFiltersExpanded] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState('');
  const [commandStatus, setCommandStatus] = useState('');
  const [preflight, setPreflight] = useState<PreflightReport | null>(null);

  const fetchRuns = useCallback(async () => {
    try {
      const data = await getPipelineRuns();
      setRuns(data ?? []);
      setLoadError('');
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : 'Не удалось загрузить задачи.');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    fetchRuns();
    getPreflight().then(setPreflight).catch(() => setPreflight(null));
  }, [fetchRuns]);

  useWebSocket({
    onEvent: () => {
      fetchRuns();
    },
  });

  // Редкий recovery fallback; live source — durable WebSocket event stream.
  useEffect(() => {
    const t = window.setInterval(fetchRuns, 30000);
    return () => window.clearInterval(t);
  }, [fetchRuns]);

  useEffect(() => {
    const refreshWhenVisible = () => {
      if (document.visibilityState === 'visible') void fetchRuns();
    };
    window.addEventListener('focus', refreshWhenVisible);
    document.addEventListener('visibilitychange', refreshWhenVisible);
    return () => {
      window.removeEventListener('focus', refreshWhenVisible);
      document.removeEventListener('visibilitychange', refreshWhenVisible);
    };
  }, [fetchRuns]);

  const needle = query.trim().toLowerCase();
  const filtered = runs
    .filter((r) => filter === 'all' || r.status === filter)
    .filter((r) => !projectFilter || String(r.project_id) === projectFilter)
    .filter((r) => !needle || r.feature.toLowerCase().includes(needle) || r.run_id.toLowerCase().includes(needle));
  const activeRuns = runs.filter((r) => activeStatuses.includes(r.status));
  const sideRuns = (activeRuns.length > 0 ? activeRuns : runs).slice(0, 5);
  const readiness = preflight?.readiness ?? (preflight?.unknown ? 'unknown' : preflight?.ready ? 'ready' : preflight ? 'blocked' : 'unknown');
  const checks = preflight?.checks ?? [];
  const failedCheck = checks.find((check) => check.status === 'failed' && check.required);

  const readinessTitle = readiness === 'ready' ? 'Окружение готово' : readiness === 'blocked' ? 'Есть проблемы' : 'Статус неизвестен';
  const readinessText = readiness === 'ready'
    ? 'Можно запускать задачи.'
    : readiness === 'blocked'
      ? failedCheck?.message || 'Исправьте блокеры, иначе задачи не стартуют.'
      : 'Проверка окружения не вернула результат. Задача может подождать worker.';

  return (
    <div className={styles.container}>
      <div className={styles.main}>
        <header className={styles.header}>
          <div>
            <h1 className={styles.title}>Очередь</h1>
            <p className={styles.subtitle}>Что в работе и что ждёт вашего решения.</p>
          </div>
        </header>

        <div className={styles.toolbar}>
          <label className={styles.search}>
            <span className={styles.visuallyHidden}>Поиск задач</span>
            <input type="search" value={query} onChange={(event) => setQuery(event.target.value)}
              placeholder="Поиск задач" />
          </label>
          <label className={styles.projectFilter}>
            <span className={styles.visuallyHidden}>Фильтр по проекту</span>
            <select value={projectFilter} onChange={(event) => setProjectFilter(event.target.value)} aria-label="Фильтр по проекту">
              <option value="">Все проекты</option>
              {projects.map((project) => <option key={project.id} value={project.id}>{projectLabel(project)}</option>)}
            </select>
          </label>
          <button
            type="button"
            className={styles.filtersToggle}
            aria-expanded={filtersExpanded}
            aria-controls="pipeline-status-filters"
            onClick={() => setFiltersExpanded((expanded) => !expanded)}
          >
            Статус: {filterLabels[filter]} · Фильтры
          </button>
          <div id="pipeline-status-filters" className={styles.filters} data-expanded={filtersExpanded} role="group" aria-label="Фильтр по статусу">
            {filters.map((f) => (
              <button
                key={f}
                className={`${styles.filterBtn} ${filter === f ? styles.active : ''}`}
                aria-pressed={filter === f}
                onClick={() => { setFilter(f); setFiltersExpanded(false); }}
              >
                {filterLabels[f]}
              </button>
            ))}
          </div>
        </div>

        {loadError && <p className={styles.commandError} role="alert">
          Не удалось загрузить задачи: {loadError} Список может быть неполным.{' '}
          <button type="button" className={styles.retryLink} onClick={() => void fetchRuns()}>Повторить загрузку</button>
        </p>}

        {loading ? (
          <div className={styles.loading}>Загружаем задачи…</div>
        ) : filtered.length === 0 ? (
          <div className={styles.empty}>
            {runs.length === 0
              ? `Задач пока нет.${canStart ? ` Нажмите «${actionVerbs.newTask}» справа — это первый шаг.` : ''}`
              : 'Ничего не найдено по этим условиям.'}
          </div>
        ) : (
          <div className={styles.pipelines}>
            {filtered.map((run) => (
              <PipelineCard key={run.id} run={run} onCancel={async () => {
                try { await cancelRun(run.run_id); await fetchRuns(); }
                catch (err) { setCommandStatus(err instanceof Error ? err.message : 'Не удалось отменить'); }
              }} />
            ))}
          </div>
        )}
      </div>

      <aside className={styles.side} aria-label="Создание и состояние">
        {canStart ? (
          <Link to="/tasks/new" className={styles.createBig}>
            <span aria-hidden="true" className={styles.plus}>+</span>
            {actionVerbs.newTask}
          </Link>
        ) : (
          <section className={styles.intakeNotice} aria-label="Создание задач">
            <strong>Создать задачу могут: {permissionRoles['run:start']}.</strong>
            <span>Передайте бизнес-цель одной из этих ролей. После уточнения требований владелец продукта согласует спецификацию до старта архитектора.</span>
          </section>
        )}

        {commandStatus && <p className={styles.commandError} role="alert">{commandStatus}</p>}

        <section className={styles.readiness} data-state={readiness} aria-labelledby="readiness-heading">
          <div className={styles.readinessHead}>
            <span className={styles.dot} aria-hidden="true" />
            <h2 id="readiness-heading">{readinessTitle}</h2>
          </div>
          <p>{readinessText}</p>
          {checks.length > 0 && (
            <details className={styles.checks}>
              <summary>Проверки окружения ({checks.length})</summary>
              {checks.map((check) => (
                <div key={check.id} data-status={check.status}>
                  <code>{check.id}</code>
                  <span>{check.status}{check.required ? ' · обязательно' : ''}</span>
                  <span>{check.message}</span>
                </div>
              ))}
            </details>
          )}
        </section>

        <section className={styles.recent} aria-labelledby="recent-heading">
          <h2 id="recent-heading">{activeRuns.length > 0 ? 'Сейчас в работе' : 'Последние задачи'}</h2>
          {sideRuns.length === 0 ? (
            <p className={styles.muted}>Пока пусто.</p>
          ) : (
            <ul className={styles.recentList}>
              {sideRuns.map((run) => (
                <li key={run.id}>
                  <Link to={`/tasks/${encodeURIComponent(run.run_id)}`}>{run.feature}</Link>
                  <span className={styles.muted} data-status={run.status}>{statusLabels[run.status] ?? run.status}</span>
                </li>
              ))}
            </ul>
          )}
        </section>
      </aside>
    </div>
  );
}
