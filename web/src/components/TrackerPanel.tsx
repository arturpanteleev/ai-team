import { useState } from 'react';
import type { FormEvent } from 'react';
import { dismissTrackerInbound, removeRunTrackerLink, retryRunTrackerDeliveries, setRunTrackerLink, syncTrackerNow } from '../api';
import { actionVerbs, allowedActionText } from '../actionLabels';
import { permissionRoles } from '../permissions';
import { inboundRequestText, trackerStatusLabels, trackerStatusTone } from '../trackerState';
import type { RunTrackerController } from '../trackerState';
import type { TrackerDelivery, TrackerInbound, TrackerSyncStatus } from '../types/tracker';
import { Icon } from './Icon';
import styles from './TrackerPanel.module.css';

const deliveryEventLabels: Record<string, string> = {
  attempt_started: 'Вход в этап',
  attempt_finished: 'Этап завершён',
  run_started: 'Задача создана',
  run_finished: 'Задача завершена',
  approval_decided: 'Решение принято',
};

const deliveryStatusLabels: Record<string, string> = {
  delivered: 'доставлено', failed: 'ошибка', abandoned: 'закрыто без доставки', superseded: 'заменено новым',
};

const inboundStateLabels: Record<string, string> = {
  received: 'получен', applied: 'выполнен', pending: 'ждёт возможности', conflict: 'конфликт',
  ignored: 'проигнорирован', superseded: 'заменён новым', dismissed: 'закрыт оператором',
};

function when(value?: string): string {
  return value ? new Date(value).toLocaleString('ru-RU') : '—';
}

function StatusLine({ status }: { status: TrackerSyncStatus }) {
  return <span className={styles.status} data-tone={trackerStatusTone[status]}>
    <Icon name={trackerStatusTone[status] === 'danger' ? 'failed' : trackerStatusTone[status] === 'ok' ? 'passed' : 'tracker'} size={12} />
    {trackerStatusLabels[status]}
  </span>;
}

// Левая колонка: где задача относительно карточки.
export function TrackerSummary({ tracker }: { tracker: RunTrackerController }) {
  const data = tracker.data;
  if (tracker.error && !data) return <section className={styles.summary} aria-label="Трекер"><h3>Трекер</h3>
    <p className={styles.errorText} role="alert">{tracker.error}</p></section>;
  if (!data || !data.configured) return null;
  const open = (data.inbound ?? []).filter((item) => item.state === 'pending' || item.state === 'conflict').length;
  const failed = (data.deliveries ?? []).filter((item) => item.status === 'failed').length;
  return <section className={styles.summary} aria-label="Трекер" data-testid="tracker-summary">
    <h3>Трекер</h3>
    <p className={styles.line}>{data.link
      ? <>Карточка <code>{data.link.issue_key}</code> · {data.tracker}</>
      : 'Карточка не выбрана'}</p>
    <StatusLine status={data.sync_status} />
    {(failed > 0 || open > 0) && <p className={styles.counts}>
      {failed > 0 && <span>Не доставлено: {failed}</span>}
      {open > 0 && <span>Входящих запросов: {open}</span>}
    </p>}
  </section>;
}

// Правая колонка: что можно сделать с трекером этой задачи.
export function TrackerActions({ runId, tracker }: { runId: string; tracker: RunTrackerController }) {
  const [issueKey, setIssueKey] = useState('');
  const data = tracker.data;
  if (!data || !data.configured) return null;
  const failed = (data.deliveries ?? []).filter((item) => item.status === 'failed').length;
  const inboundOn = tracker.overview?.inbound?.configured === true;
  const link = async (event: FormEvent) => {
    event.preventDefault();
    if (await tracker.act(actionVerbs.linkTracker, () => setRunTrackerLink(runId, issueKey.trim()))) setIssueKey('');
  };
  return <section className={styles.actions} aria-label="Действия с трекером">
    <h3>Трекер</h3>
    {!tracker.canLink && <p className={styles.hint}>Связь с карточкой меняют: {permissionRoles['tracker:link']}.</p>}
    {tracker.canLink && !data.link && <form className={styles.linkForm} onSubmit={(event) => void link(event)}>
      <label>Ключ карточки
        <input value={issueKey} onChange={(event) => setIssueKey(event.target.value)} placeholder="DEMO-1" maxLength={64} required />
      </label>
      <button disabled={tracker.busy || !issueKey.trim()}>{actionVerbs.linkTracker}</button>
    </form>}
    {tracker.canLink && data.link && <button type="button" disabled={tracker.busy}
      onClick={() => void tracker.act(actionVerbs.unlinkTracker, () => removeRunTrackerLink(runId))}>
      {actionVerbs.unlinkTracker}
    </button>}
    {tracker.canLink && data.link && failed > 0 && <button type="button" disabled={tracker.busy}
      onClick={() => void tracker.act(actionVerbs.retryTracker, () => retryRunTrackerDeliveries(runId))}>
      {actionVerbs.retryTracker} ({failed})
    </button>}
    {tracker.canLink && inboundOn && <button type="button" disabled={tracker.busy}
      onClick={() => void tracker.act(actionVerbs.syncTracker, syncTrackerNow)}>
      {actionVerbs.syncTracker}
    </button>}
    {tracker.status && <p role="status" className={styles.hint}>{tracker.status}</p>}
    {tracker.error && <p role="alert" className={styles.errorText}>{tracker.error}</p>}
  </section>;
}

function DeliveryRow({ delivery }: { delivery: TrackerDelivery }) {
  return <li className={styles.row} data-failed={delivery.status === 'failed' ? 'true' : undefined}>
    <strong>{deliveryEventLabels[delivery.event_type] ?? delivery.event_type}{delivery.stage ? ` · ${delivery.stage}` : ''}</strong>
    <span>{deliveryStatusLabels[delivery.status] ?? delivery.status}
      {delivery.transition_to ? ` · карточка переведена в «${delivery.transition_to}»` : ''}
      {delivery.attempts > 1 ? ` · попыток: ${delivery.attempts}` : ''} · {when(delivery.updated_at)}</span>
    {delivery.last_error && delivery.status === 'failed' && <span className={styles.errorText}>{delivery.last_error}</span>}
    {delivery.note && <span className={styles.hint}>{delivery.note}</span>}
  </li>;
}

function InboundRow({ request, runId, tracker }: { request: TrackerInbound; runId: string; tracker: RunTrackerController }) {
  const [reason, setReason] = useState('');
  const open = request.state === 'pending' || request.state === 'conflict';
  const allowed = request.allowed_actions ?? [];
  return <li className={styles.row} data-state={request.state}>
    <strong>{inboundRequestText(request)}</strong>
    <span>{inboundStateLabels[request.state] ?? request.state} · {request.author || request.origin} · {when(request.changed_at)}</span>
    {request.reason && <span>Причина: {request.reason}</span>}
    {open && request.suggested_action && <span>Допустимо сейчас: {request.suggested_action}</span>}
    {open && allowed.length > 0 && <span className={styles.hint}>Контроллер разрешает: {allowed.map(allowedActionText).join(', ')}. Статус задачи карточкой не подменяется.</span>}
    {open && tracker.canLink && <form className={styles.dismiss} onSubmit={(event) => {
      event.preventDefault();
      void tracker.act(actionVerbs.dismissInbound, () => dismissTrackerInbound(runId, request.change_id, reason.trim()));
    }}>
      <label>Причина закрытия
        <input value={reason} onChange={(event) => setReason(event.target.value)} required maxLength={2000} />
      </label>
      <button disabled={tracker.busy || !reason.trim()}>{actionVerbs.dismissInbound}</button>
    </form>}
  </li>;
}

// Основная область: связь, доставка, конфликты и входящие запросы.
export function TrackerDetails({ runId, tracker }: { runId: string; tracker: RunTrackerController }) {
  const data = tracker.data;
  if (tracker.error && !data) return <p role="alert" className={styles.errorText}>{tracker.error}</p>;
  if (!data) return <p>Загружаем состояние трекера…</p>;
  if (!data.configured) return <p>Трекер не подключён. Подключение — блок <code>tracker</code> в конфигурации проекта.</p>;
  const poll = tracker.overview?.inbound;
  const conflicts = data.conflicts ?? [];
  const inbound = data.inbound ?? [];
  const deliveries = data.deliveries ?? [];
  return <div className={styles.details}>
    <p><StatusLine status={data.sync_status} />
      {data.link && <> · карточка <code>{data.link.issue_key}</code> ({data.tracker}), связал {data.link.linked_by || '—'}, {when(data.link.linked_at)}</>}</p>
    {poll?.configured && <p className={styles.hint}>
      Входящие изменения: опрос раз в {poll.poll_interval_seconds} с
      {poll.delay_seconds !== undefined ? `, отставание ${poll.delay_seconds} с` : ''}
      {poll.status === 'never_polled' ? ', опроса ещё не было' : ''}
      {poll.status === 'failed' && poll.sync?.last_error ? `. Последняя ошибка: ${poll.sync.last_error}` : ''}.
    </p>}
    {poll && !poll.configured && <p className={styles.hint}>Входящая синхронизация не настроена: изменения карточки задачу не двигают.</p>}

    {conflicts.length > 0 && <section aria-label="Конфликты связи">
      <h3>Конфликты связи</h3>
      <ul className={styles.list}>{conflicts.map((conflict) => <li key={conflict.issue_key} className={styles.row} data-state="conflict">
        <strong><code>{conflict.issue_key}</code></strong><span>{conflict.reason}</span>
        <span className={styles.hint}>Выберите карточку явно в колонке «Действия» или снимите связь.</span>
      </li>)}</ul>
    </section>}

    <section aria-label="Входящие запросы трекера">
      <h3>Входящие запросы</h3>
      {inbound.length === 0 ? <p>Запросов от трекера по этой задаче нет.</p>
        : <ul className={styles.list}>{inbound.map((request) => <InboundRow key={request.change_id} request={request} runId={runId} tracker={tracker} />)}</ul>}
    </section>

    <section aria-label="Отправка в трекер">
      <h3>Отправка в трекер</h3>
      {deliveries.length === 0 ? <p>Событий для отправки пока не было.</p>
        : <ul className={styles.list}>{deliveries.map((delivery) => <DeliveryRow key={delivery.event_id} delivery={delivery} />)}</ul>}
    </section>
  </div>;
}
