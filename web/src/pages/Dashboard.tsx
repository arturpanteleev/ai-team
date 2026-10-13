import { useState, useEffect, useCallback, useRef } from 'react';
import type { FormEvent } from 'react';
import type { PipelineRun, PipelineStatus, PreflightReport } from '../types';
import { cancelRun, getActivePrincipal, getPipelineRuns, getPreflight, startRun } from '../api';
import { useWebSocket } from '../hooks/useWebSocket';
import { PipelineCard } from '../components/PipelineCard';
import { Link } from '../router';
import { statusLabels } from '../statusLabels';
import { usePageTitle } from '../hooks/usePageTitle';
import { identifierError, identifierFromTitle, identifierHint, isIdentifierServerError } from '../identifier';
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

export function Dashboard() {
  usePageTitle('Задачи');
  const principal = getActivePrincipal();
  const canSubmitIntention = !principal || principal.roles.includes('product_owner');
  const [runs, setRuns] = useState<PipelineRun[]>([]);
  const [filter, setFilter] = useState<Filter>('all');
  const [query, setQuery] = useState('');
  const [filtersExpanded, setFiltersExpanded] = useState(false);
  const [loading, setLoading] = useState(true);
  const [creating, setCreating] = useState(false);
  const [createdRunID, setCreatedRunID] = useState('');
  const [name, setName] = useState('');
  const [feature, setFeature] = useState('');
  // Пока пользователь не правил идентификатор вручную, он следует за названием.
  const [featureEdited, setFeatureEdited] = useState(false);
  const [featureError, setFeatureError] = useState('');
  const featureRef = useRef<HTMLInputElement>(null);
  const [task, setTask] = useState('');
  const [commandStatus, setCommandStatus] = useState('');
  const [preflight, setPreflight] = useState<PreflightReport | null>(null);

  const fetchRuns = useCallback(async () => {
    try {
      const data = await getPipelineRuns();
      setRuns(data ?? []);
    } catch (err) {
      console.error('Failed to fetch pipelines:', err);
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

  const submitRun = async (event: FormEvent) => {
    event.preventDefault();
    const idError = identifierError(feature);
    if (idError) {
      setFeatureError(idError);
      featureRef.current?.focus();
      return;
    }
    setCommandStatus('Создаём задачу…');
    try {
      const result = await startRun(feature, task);
      setCreatedRunID(result.run_id);
      setCommandStatus('');
      setCreating(false);
      setName('');
      setFeature('');
      setFeatureEdited(false);
      setFeatureError('');
      setTask('');
      fetchRuns();
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Команда не принята';
      if (isIdentifierServerError(message)) {
        setFeatureError(message);
        featureRef.current?.focus();
      } else {
        setCommandStatus(message);
      }
    }
  };

  const createForm = (
    <form className={styles.createCard} onSubmit={submitRun} aria-labelledby="create-heading">
      <div className={styles.createHead}>
        <h2 id="create-heading">Новая задача</h2>
        <button type="button" className={styles.cancelCreate} onClick={() => { setCreating(false); setCommandStatus(''); }}>Отмена</button>
      </div>
      <label htmlFor="initiative-name">Название задачи</label>
      <input id="initiative-name" value={name} onChange={(event) => {
        setName(event.target.value);
        setFeatureError('');
        if (!featureEdited) setFeature(identifierFromTitle(event.target.value));
      }} placeholder="например, вход через Google" required />
      <label htmlFor="feature-name">Короткий идентификатор</label>
      <input id="feature-name" ref={featureRef} value={feature} onChange={(event) => {
        setFeatureEdited(true);
        setFeatureError('');
        setFeature(event.target.value);
      }} placeholder="google-auth" aria-invalid={featureError ? true : undefined}
        aria-describedby="feature-name-hint feature-name-error" />
      <small id="feature-name-hint">{identifierHint}</small>
      {featureError && <span id="feature-name-error" role="alert" className={styles.fieldError}>{featureError}</span>}
      <label htmlFor="business-intention">Какого результата хотите достичь?</label>
      <textarea id="business-intention" value={task} onChange={(event) => setTask(event.target.value)}
        placeholder="Опишите цель, ограничения и ожидаемый результат. Аналитик уточнит недостающие детали."
        rows={5} required />
      <button type="submit" className={styles.createSubmit} disabled={readiness === 'blocked'}>Создать задачу</button>
      <small>Намерение и уточнения сохраняются в истории задачи. До технического планирования Product Owner согласует спецификацию.</small>
      {commandStatus && <span role="status">{commandStatus}</span>}
    </form>
  );

  return (
    <div className={styles.container}>
      <div className={styles.main}>
        <header className={styles.header}>
          <div>
            <h1 className={styles.title}>Задачи</h1>
            <p className={styles.subtitle}>Что в работе и что ждёт вашего решения.</p>
          </div>
        </header>

        <div className={styles.toolbar}>
          <label className={styles.search}>
            <span className={styles.visuallyHidden}>Поиск задач</span>
            <input type="search" value={query} onChange={(event) => setQuery(event.target.value)}
              placeholder="Поиск задач" />
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

        {loading ? (
          <div className={styles.loading}>Загружаем задачи…</div>
        ) : filtered.length === 0 ? (
          <div className={styles.empty}>
            {runs.length === 0 ? 'Задач пока нет. Создайте первую справа.' : 'Ничего не найдено по этим условиям.'}
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
        {canSubmitIntention ? (
          creating ? createForm : (
            <button type="button" className={styles.createBig} onClick={() => setCreating(true)}>
              <span aria-hidden="true" className={styles.plus}>+</span>
              Создать задачу
            </button>
          )
        ) : (
          <section className={styles.intakeNotice} aria-label="Создание задач">
            <strong>Создать задачу может Product Owner.</strong>
            <span>Передайте бизнес-цель Product Owner. После уточнения требований он согласует спецификацию до старта архитектора.</span>
          </section>
        )}

        {createdRunID && !creating && (
          <section className={styles.created} role="status">
            <strong>Задача создана</strong>
            <Link to={`/tasks/${encodeURIComponent(createdRunID)}`}>Открыть задачу →</Link>
          </section>
        )}

        {!creating && commandStatus && <p className={styles.commandError} role="alert">{commandStatus}</p>}

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
