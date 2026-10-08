import { useNavigate } from '../router';
import type { PipelineRun } from '../types';
import { StatusBadge } from './StatusBadge';
import styles from './PipelineCard.module.css';

interface PipelineCardProps {
  run: PipelineRun;
  onCancel?: () => void;
}

export function PipelineCard({ run, onCancel }: PipelineCardProps) {
  const navigate = useNavigate();

  const duration = run.completed_at
    ? ((new Date(run.completed_at).getTime() - new Date(run.started_at).getTime()) / 1000).toFixed(1) + 's'
    : '—';

  const time = new Date(run.started_at).toLocaleString('ru-RU', {
    day: '2-digit',
    month: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });

  return (
    <article
      className={styles.card}
      onClick={() => navigate(`/pipelines/${run.id}`)}
      onKeyDown={(event) => {
        if (event.target === event.currentTarget && (event.key === 'Enter' || event.key === ' ')) {
          event.preventDefault();
          navigate(`/pipelines/${run.id}`);
        }
      }}
      role="link"
      tabIndex={0}
      aria-label={`Открыть задачу: ${run.feature}`}
    >
      <div className={styles.header}>
        <span className={styles.feature}>{run.feature}</span>
        <StatusBadge status={run.status} />
      </div>
      <div className={styles.footer}>
        <span>{time}</span>
        <span className={styles.duration}>{duration}</span>
        {run.queue_job_id ? <span>Очередь №{run.queue_job_id}</span> : null}
        {run.error ? <span role="alert">{run.error}</span> : null}
        {run.status === 'queued' && onCancel ? <button onClick={(event) => {
          event.stopPropagation(); onCancel();
        }}>Отменить</button> : null}
      </div>
    </article>
  );
}
