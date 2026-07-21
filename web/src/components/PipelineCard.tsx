import { useNavigate } from 'react-router-dom';
import type { PipelineRun } from '../types';
import { StatusBadge } from './StatusBadge';
import styles from './PipelineCard.module.css';

interface PipelineCardProps {
  run: PipelineRun;
}

export function PipelineCard({ run }: PipelineCardProps) {
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
    <div className={styles.card} onClick={() => navigate(`/pipelines/${run.id}`)}>
      <div className={styles.header}>
        <span className={styles.feature}>{run.feature}</span>
        <StatusBadge status={run.status} />
      </div>
      <div className={styles.footer}>
        <span>{time}</span>
        <span className={styles.duration}>{duration}</span>
      </div>
    </div>
  );
}