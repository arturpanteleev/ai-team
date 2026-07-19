import styles from './StatusBadge.module.css';
import type { PipelineStatus, StageStatus } from '../types';

interface StatusBadgeProps {
  status: PipelineStatus | StageStatus;
}

export function StatusBadge({ status }: StatusBadgeProps) {
  return (
    <span className={`${styles.badge} ${styles[status] || styles.pending}`}>
      {status}
    </span>
  );
}