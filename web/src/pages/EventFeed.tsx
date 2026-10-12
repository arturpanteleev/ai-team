import { useCallback, useEffect, useRef, useState } from 'react';
import { getGlobalEvents } from '../api';
import { Link } from '../router';
import { useWebSocket } from '../hooks/useWebSocket';
import { usePageTitle } from '../hooks/usePageTitle';
import type { TaskEvent, WsEvent } from '../types';
import styles from './EventFeed.module.css';

const PAGE_SIZE = 100;

const warningTypes = new Set([
  'check_overridden', 'check_error', 'description_missing', 'stage_skipped',
  'stage_stalled', 'integration_error',
]);

const eventLabels: Record<string, string> = {
  run_started: 'Задача создана',
  run_resumed: 'Задача возобновлена',
  run_paused: 'Задача приостановлена',
  run_canceled: 'Задача отменена',
  run_finished: 'Задача завершена',
  attempt_started: 'Этап начат',
  attempt_finished: 'Этап завершён',
  attempts_invalidated: 'Результаты этапов сброшены',
  approval_requested: 'Запрошено подтверждение',
  approval_decided: 'Решение по подтверждению',
  transition_selected: 'Выбран переход',
  stage_taken: 'Этап принят в работу',
  check_overridden: 'Проверка переопределена',
  check_error: 'Ошибка проверки',
  description_missing: 'Не хватает описания',
  stage_skipped: 'Этап пропущен',
  stage_stalled: 'Этап остановился без результата',
  integration_error: 'Ошибка интеграции',
};

function isWarningEvent(event: Pick<TaskEvent, 'type' | 'severity' | 'data'>): boolean {
  return event.severity?.toLowerCase() === 'warning' ||
    (typeof event.data.severity === 'string' && event.data.severity.toLowerCase() === 'warning') ||
    warningTypes.has(event.type);
}

function mergeEvents(existing: TaskEvent[], incoming: TaskEvent[]): TaskEvent[] {
  const byCursor = new Map<number, TaskEvent>();
  for (const event of existing) byCursor.set(event.cursor, event);
  for (const event of incoming) byCursor.set(event.cursor, event);
  return [...byCursor.values()].sort((left, right) => left.cursor - right.cursor);
}

function dataLabel(data: Record<string, unknown>): string {
  for (const key of ['feature', 'title', 'task']) {
    const value = data[key];
    if (typeof value === 'string' && value.trim()) return value;
  }
  return '';
}

function eventDescription(event: TaskEvent): string {
  for (const key of ['reason', 'error', 'message']) {
    const value = event.data[key];
    if (typeof value === 'string' && value.trim()) return value;
  }
  return '';
}

export function EventFeed() {
  usePageTitle('Лента событий');
  const [warningsOnly, setWarningsOnly] = useState(false);
  const [events, setEvents] = useState<TaskEvent[]>([]);
  const [cursor, setCursor] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState('');
  const requestGeneration = useRef(0);

  useEffect(() => {
    const generation = ++requestGeneration.current;
    setEvents([]);
    setCursor(0);
    setHasMore(false);
    setLoadingMore(false);
    setError('');
    setLoading(true);
    void getGlobalEvents(0, PAGE_SIZE, warningsOnly)
      .then((page) => {
        if (generation !== requestGeneration.current) return;
        const pageEvents = page.events ?? [];
        setEvents((current) => mergeEvents(current, pageEvents));
        setCursor(page.next_cursor ?? 0);
        setHasMore(pageEvents.length === PAGE_SIZE);
      })
      .catch(() => {
        if (generation === requestGeneration.current) setError('Не удалось загрузить события. Попробуйте обновить ленту.');
      })
      .finally(() => {
        if (generation === requestGeneration.current) setLoading(false);
      });
    return () => {
      requestGeneration.current += 1;
    };
  }, [warningsOnly]);

  const onLiveEvent = useCallback((event: WsEvent) => {
    if (warningsOnly && !isWarningEvent(event)) return;
    setEvents((current) => mergeEvents(current, [event]));
  }, [warningsOnly]);

  const changeWarningsOnly = useCallback((next: boolean) => {
    if (next === warningsOnly) return;
    // Invalidate in-flight pagination immediately, before the next effect runs.
    requestGeneration.current += 1;
    setWarningsOnly(next);
  }, [warningsOnly]);

  useWebSocket({ onEvent: onLiveEvent });

  const loadMore = useCallback(async () => {
    if (loadingMore || !hasMore) return;
    const generation = requestGeneration.current;
    setLoadingMore(true);
    setError('');
    try {
      const page = await getGlobalEvents(cursor, PAGE_SIZE, warningsOnly);
      if (generation !== requestGeneration.current) return;
      const pageEvents = page.events ?? [];
      setEvents((current) => mergeEvents(current, pageEvents));
      setCursor((current) => Math.max(current, page.next_cursor ?? current));
      setHasMore(pageEvents.length === PAGE_SIZE);
    } catch {
      if (generation === requestGeneration.current) setError('Не удалось загрузить следующие события. Попробуйте ещё раз.');
    } finally {
      if (generation === requestGeneration.current) setLoadingMore(false);
    }
  }, [cursor, hasMore, loadingMore, warningsOnly]);

  return (
    <section className={styles.container} aria-labelledby="event-feed-title">
      <header className={styles.header}>
        <div>
          <h1 id="event-feed-title" className={styles.title}>Лента событий</h1>
          <p className={styles.subtitle}>Обновляется автоматически по мере поступления событий.</p>
        </div>
        <div className={styles.filters} role="group" aria-label="Фильтр событий">
          <button type="button" aria-pressed={!warningsOnly} onClick={() => changeWarningsOnly(false)}>Все</button>
          <button type="button" aria-pressed={warningsOnly} onClick={() => changeWarningsOnly(true)}>Предупреждения</button>
        </div>
      </header>

      {error && <p className={styles.error} role="alert">{error}</p>}
      {loading ? (
        <p className={styles.empty} role="status">Загружаем события…</p>
      ) : events.length === 0 ? (
        <p className={styles.empty}>Событий пока нет.</p>
      ) : (
        <ol className={styles.list} aria-label="Список событий">
          {events.map((event) => {
            const warning = isWarningEvent(event);
            const description = eventDescription(event);
            const taskName = dataLabel(event.data) || event.run_id;
            return (
              <li key={event.cursor}>
                <article className={styles.event} data-warning={warning}>
                  <div className={styles.eventHeader}>
                    <strong>{eventLabels[event.type] ?? event.type}</strong>
                    {warning && <span className={styles.warning}>Предупреждение</span>}
                    <time dateTime={event.timestamp}>{new Date(event.timestamp).toLocaleString('ru-RU')}</time>
                  </div>
                  <p className={styles.task}>
                    <Link to={`/runs/${encodeURIComponent(event.run_id)}`}>Задача: {taskName}</Link>
                  </p>
                  <div className={styles.meta}>
                    {event.stage && <span>Этап: {event.stage}</span>}
                    {event.actor && <span>Участник: {event.actor}</span>}
                    {event.executor && <span>Исполнитель: {event.executor}</span>}
                    {event.attempt_id && <span>Попытка: {event.attempt_id}</span>}
                  </div>
                  {description && <p className={styles.description}>{description}</p>}
                </article>
              </li>
            );
          })}
        </ol>
      )}

      {!loading && hasMore && (
        <button type="button" className={styles.more} disabled={loadingMore} onClick={() => void loadMore()}>
          {loadingMore ? 'Загружаем…' : 'Показать ещё'}
        </button>
      )}
    </section>
  );
}
