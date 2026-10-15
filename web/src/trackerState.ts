import { useCallback, useEffect, useRef, useState } from 'react';
import { getActivePrincipal, getRunTracker, getTrackerOverview } from './api';
import { mayPerform } from './permissions';
import type { RunTracker, TrackerInbound, TrackerOverview, TrackerSyncStatus } from './types/tracker';

export const trackerStatusLabels: Record<TrackerSyncStatus, string> = {
  not_configured: 'Трекер не подключён',
  not_linked: 'Не связана с карточкой',
  conflict: 'Конфликт связи',
  failed: 'Ошибка отправки в трекер',
  inbound_pending: 'Есть входящий запрос трекера',
  ok: 'Синхронизировано',
};

// Красный — только реальный сбой доставки; запросы и конфликты ждут решения человека.
export const trackerStatusTone: Record<TrackerSyncStatus, 'neutral' | 'danger' | 'attention' | 'ok'> = {
  not_configured: 'neutral', not_linked: 'neutral', conflict: 'attention', failed: 'danger', inbound_pending: 'attention', ok: 'ok',
};

export function inboundRequestText(request: TrackerInbound): string {
  const target = request.action === 'return'
    ? `вернуть на этап «${request.to_stage ?? '?'}»`
    : request.action === 'advance' ? 'продолжить этап' : request.action || 'запрос';
  return `Статус карточки «${request.from_status || '—'}» → «${request.to_status}»: ${target}`;
}

export interface RunTrackerController {
  data: RunTracker | null;
  overview: TrackerOverview | null;
  error: string;
  status: string;
  busy: boolean;
  canLink: boolean;
  reload: () => Promise<void>;
  act: (label: string, action: () => Promise<unknown>) => Promise<boolean>;
}

// Состояние трекера одной задачи. Перезагружается вместе со страницей (reloadKey).
export function useRunTracker(runId: string, reloadKey: unknown): RunTrackerController {
  const [data, setData] = useState<RunTracker | null>(null);
  const [overview, setOverview] = useState<TrackerOverview | null>(null);
  const [error, setError] = useState('');
  const [status, setStatus] = useState('');
  const [busy, setBusy] = useState(false);
  const current = useRef(runId);
  current.current = runId;

  const reload = useCallback(async () => {
    const requested = runId;
    try {
      const [tracker, summary] = await Promise.all([
        getRunTracker(requested),
        getTrackerOverview().catch(() => null),
      ]);
      if (current.current !== requested) return;
      setData(tracker);
      setOverview(summary);
      setError('');
    } catch (reason) {
      if (current.current === requested) setError(reason instanceof Error ? reason.message : 'Не удалось получить состояние трекера.');
    }
  }, [runId]);

  useEffect(() => { setData(null); setOverview(null); setStatus(''); setError(''); }, [runId]);
  useEffect(() => { void reload(); }, [reload, reloadKey]);

  const act = async (label: string, action: () => Promise<unknown>) => {
    setBusy(true);
    setError('');
    setStatus(`${label}…`);
    try {
      await action();
      setStatus('');
      await reload();
      return true;
    } catch (reason) {
      setStatus('');
      setError(reason instanceof Error ? reason.message : `Не удалось выполнить действие: ${label.toLowerCase()}.`);
      await reload();
      return false;
    } finally {
      setBusy(false);
    }
  };

  return { data, overview, error, status, busy, canLink: mayPerform(getActivePrincipal(), 'tracker:link'), reload, act };
}
