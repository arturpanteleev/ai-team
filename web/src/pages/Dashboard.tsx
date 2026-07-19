import { useState, useEffect, useCallback } from 'react';
import type { PipelineRun, PipelineStatus } from '../types';
import { getPipelineRuns } from '../api';
import { useWebSocket } from '../hooks/useWebSocket';
import { PipelineCard } from '../components/PipelineCard';
import styles from './Dashboard.module.css';

type Filter = 'all' | PipelineStatus;

export function Dashboard() {
  const [runs, setRuns] = useState<PipelineRun[]>([]);
  const [filter, setFilter] = useState<Filter>('all');
  const [loading, setLoading] = useState(true);

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
  }, [fetchRuns]);

  useWebSocket({
    onEvent: (event) => {
      if (event.type === 'pipeline_completed') {
        fetchRuns();
      }
    },
  });

  const filtered = filter === 'all' ? runs : runs.filter((r) => r.status === filter);

  return (
    <div className={styles.container}>
      <div className={styles.header}>
        <h1 className={styles.title}>Pipeline Runs</h1>
        <div className={styles.filters}>
          {(['all', 'running', 'completed', 'failed'] as Filter[]).map((f) => (
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

      {loading ? (
        <div className={styles.loading}>Loading...</div>
      ) : filtered.length === 0 ? (
        <div className={styles.empty}>No pipeline runs found</div>
      ) : (
        <div className={styles.pipelines}>
          {filtered.map((run) => (
            <PipelineCard key={run.id} run={run} />
          ))}
        </div>
      )}
    </div>
  );
}