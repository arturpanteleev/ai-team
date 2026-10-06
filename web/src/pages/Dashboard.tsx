import { useState, useEffect, useCallback } from 'react';
import type { FormEvent } from 'react';
import type { PipelineRun, PipelineStatus, PreflightReport } from '../types';
import { cancelRun, getActivePrincipal, getPipelineRuns, getPreflight, startRun } from '../api';
import { useWebSocket } from '../hooks/useWebSocket';
import { PipelineCard } from '../components/PipelineCard';
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

export function Dashboard() {
  const principal = getActivePrincipal();
  const canSubmitIntention = !principal || principal.roles.includes('product_owner');
  const [runs, setRuns] = useState<PipelineRun[]>([]);
  const [filter, setFilter] = useState<Filter>('all');
  const [loading, setLoading] = useState(true);
  const [feature, setFeature] = useState('');
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

  const filtered = filter === 'all' ? runs : runs.filter((r) => r.status === filter);
  const readiness = preflight?.readiness ?? (preflight?.unknown ? 'unknown' : preflight?.ready ? 'ready' : preflight ? 'blocked' : 'unknown');

  const submitRun = async (event: FormEvent) => {
    event.preventDefault();
    setCommandStatus('Запускаю…');
    try {
      const result = await startRun(feature, task);
      setCommandStatus(`Run принят: ${result.run_id}`);
      setFeature('');
      setTask('');
      fetchRuns();
    } catch (err) {
      setCommandStatus(err instanceof Error ? err.message : 'Команда не принята');
    }
  };

  return (
    <div className={styles.container}>
      <div className={styles.header}>
        <h1 className={styles.title}>Pipeline Runs</h1>
        <div className={styles.filters}>
          {filters.map((f) => (
            <button
              key={f}
              className={`${styles.filterBtn} ${filter === f ? styles.active : ''}`}
              onClick={() => setFilter(f)}
            >
              {f}
            </button>
          ))}
        </div>
      </div>

      {canSubmitIntention ? (
        <form className={styles.runForm} onSubmit={submitRun}>
          <label htmlFor="feature-name">Название инициативы</label>
          <input id="feature-name" value={feature} onChange={(event) => setFeature(event.target.value)}
            placeholder="например, рост повторных продаж" required />
          <label htmlFor="business-intention">Какого результата хотите достичь?</label>
          <textarea id="business-intention" value={task} onChange={(event) => setTask(event.target.value)}
            placeholder="Опишите бизнес-цель, ограничения и ожидаемый результат своими словами. Аналитик уточнит недостающие детали."
            rows={4} required />
          <button type="submit" disabled={readiness === 'blocked'}>Создать инициативу и передать аналитику</button>
          <small>Ваше намерение и все последующие уточнения сохраняются в истории задачи. До начала технического планирования Product Owner согласует спецификацию.</small>
          {commandStatus && <span>{commandStatus}</span>}
        </form>
      ) : (
        <section className={styles.intakeNotice}>
          <strong>Создать инициативу может Product Owner.</strong>
          <span>Передайте бизнес-цель Product Owner. После уточнения требований он согласует спецификацию до старта архитектора.</span>
        </section>
      )}

      <section className={styles.preflight}>
        <strong>Готовность окружения: {readiness === 'ready' ? 'готово' : readiness === 'blocked' ? 'есть блокеры' : 'неизвестна · задача может ждать worker'}</strong>
        {(preflight?.checks ?? []).map((check) => (
          <div key={check.id} data-status={check.status}>
            <code>{check.id}</code>
            <span>{check.status}{check.required ? ' · required' : ''}</span>
            <span>{check.message}</span>
          </div>
        ))}
      </section>

      {loading ? (
        <div className={styles.loading}>Loading...</div>
      ) : filtered.length === 0 ? (
        <div className={styles.empty}>No pipeline runs found</div>
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
  );
}
