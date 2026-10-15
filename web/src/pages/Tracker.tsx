import { useCallback, useEffect, useState } from 'react';
import { getTrackerDeliveries, getTrackerInbound, getTrackerOverview, syncTrackerNow } from '../api';
import { actionVerbs } from '../actionLabels';
import { inboundRequestText } from '../trackerState';
import { mayPerform, permissionRoles } from '../permissions';
import { getActivePrincipal } from '../api';
import { usePageTitle } from '../hooks/usePageTitle';
import { Link } from '../router';
import type { TrackerDelivery, TrackerInbound, TrackerOverview, TrackerSyncResult } from '../types/tracker';
import styles from './Tracker.module.css';

function when(value?: string): string {
  return value ? new Date(value).toLocaleString('ru-RU') : '—';
}

const pollStatusLabels: Record<string, string> = {
  never_polled: 'опроса ещё не было',
  ok: 'работает',
  failed: 'последний опрос завершился ошибкой',
};

// Обзор трекера по всем задачам: состояние опроса, открытые входящие запросы и
// недоставленные события. Связь конкретной задачи с карточкой — на странице задачи.
export function Tracker() {
  usePageTitle('Трекер');
  const canLink = mayPerform(getActivePrincipal(), 'tracker:link');
  const [overview, setOverview] = useState<TrackerOverview | null>(null);
  const [requests, setRequests] = useState<TrackerInbound[]>([]);
  const [failed, setFailed] = useState<TrackerDelivery[]>([]);
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(true);
  const [syncing, setSyncing] = useState(false);
  const [result, setResult] = useState<TrackerSyncResult | null>(null);

  const load = useCallback(async () => {
    try {
      const summary = await getTrackerOverview();
      setOverview(summary);
      if (summary.configured) {
        const [inbound, deliveries] = await Promise.all([getTrackerInbound('open'), getTrackerDeliveries('failed')]);
        setRequests(inbound.requests ?? []);
        setFailed(deliveries.deliveries ?? []);
      }
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Не удалось получить состояние трекера.');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const sync = async () => {
    setSyncing(true);
    setResult(null);
    setError('');
    try {
      const response = await syncTrackerNow();
      setResult(response.result);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Не удалось синхронизировать.');
    } finally {
      setSyncing(false);
      await load();
    }
  };

  if (loading) return <main className={styles.page}><p>Загружаем состояние трекера…</p></main>;
  if (error && !overview) return <main className={styles.page}><h1>Трекер</h1><p role="alert" className={styles.error}>{error}</p>
    <button type="button" onClick={() => { setError(''); void load(); }}>Повторить загрузку</button></main>;
  if (!overview?.configured) {
    return <main className={styles.page}>
      <h1>Трекер</h1>
      <p>Трекер задач не подключён. Подключение — блок <code>tracker</code> в конфигурации проекта; после этого задачу можно связать с одной карточкой, а статусы будут отправляться автоматически.</p>
    </main>;
  }
  const inbound = overview.inbound;
  return (
    <main className={styles.page}>
      <h1>Трекер</h1>
      <p>Подключён: <strong>{overview.tracker}</strong>. Связь с карточкой и повтор отправки — на странице задачи, вкладка «Трекер».</p>
      {error && <p role="alert" className={styles.error}>{error}</p>}

      <section className={styles.card} aria-labelledby="tracker-sync-heading">
        <h2 id="tracker-sync-heading">Входящая синхронизация</h2>
        {inbound?.configured ? <>
          <p>Опрос раз в {inbound.poll_interval_seconds} с · {pollStatusLabels[inbound.status ?? ''] ?? inbound.status}
            {inbound.delay_seconds !== undefined ? ` · отставание ${inbound.delay_seconds} с` : ''}.</p>
          {inbound.status === 'failed' && inbound.sync?.last_error && <p className={styles.error}>Последняя ошибка: {inbound.sync.last_error}</p>}
          {inbound.sync?.last_success_at && <p className={styles.muted}>Последний успешный опрос: {when(inbound.sync.last_success_at)}</p>}
          {canLink
            ? <button type="button" disabled={syncing} onClick={() => void sync()}>{syncing ? 'Синхронизируем…' : actionVerbs.syncTracker}</button>
            : <p className={styles.muted}>Запускать синхронизацию могут: {permissionRoles['tracker:link']}.</p>}
          {result && <p role="status" className={styles.muted}>
            {result.coalesced ? 'Недавний опрос уже выполнен, повтор не нужен. ' : ''}
            Изменений: {result.changes}, выполнено: {result.applied}, ждёт: {result.pending}, конфликтов: {result.conflicts}, проигнорировано: {result.ignored}.</p>}
        </> : <p>Входящая синхронизация не настроена (<code>tracker.inbound</code>): изменения карточек задачи не двигают.</p>}
      </section>

      <section className={styles.card} aria-labelledby="tracker-open-heading">
        <h2 id="tracker-open-heading">Открытые входящие запросы ({requests.length})</h2>
        {requests.length === 0 ? <p>Открытых запросов нет.</p> : <ul className={styles.list}>
          {requests.map((request) => <li key={`${request.task_id}:${request.change_id}`} className={styles.row} data-state={request.state}>
            <strong><Link to={`/pipelines/${request.task_id}`}>Карточка {request.issue_key}</Link></strong>
            <span>{inboundRequestText(request)}</span>
            <span className={styles.muted}>{request.state === 'conflict' ? 'Конфликт' : 'Ждёт возможности'}{request.reason ? `: ${request.reason}` : ''}</span>
            {request.suggested_action && <span className={styles.muted}>Допустимо сейчас: {request.suggested_action}</span>}
          </li>)}
        </ul>}
      </section>

      <section className={styles.card} aria-labelledby="tracker-failed-heading">
        <h2 id="tracker-failed-heading">Не доставлено в трекер ({failed.length})</h2>
        {failed.length === 0 ? <p>Недоставленных событий нет.</p> : <ul className={styles.list}>
          {failed.map((delivery) => <li key={delivery.event_id} className={styles.row} data-failed="true">
            <strong><Link to={`/pipelines/${delivery.task_id}`}>Карточка {delivery.issue_key}</Link></strong>
            <span>{delivery.event_type}{delivery.stage ? ` · ${delivery.stage}` : ''} · попыток: {delivery.attempts}</span>
            {delivery.last_error && <span className={styles.error}>{delivery.last_error}</span>}
          </li>)}
        </ul>}
        {failed.length > 0 && <p className={styles.muted}>Повторить отправку можно на странице задачи: {permissionRoles['tracker:link']}.</p>}
      </section>
    </main>
  );
}
