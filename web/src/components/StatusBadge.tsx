import styles from './StatusBadge.module.css';
import type { PipelineStatus, StageStatus } from '../types';
import { statusLabels } from '../statusLabels';

interface StatusBadgeProps {
  status: PipelineStatus | StageStatus;
}

export function StatusBadge({ status }: StatusBadgeProps) {
  return (
    <span className={`${styles.badge} ${styles[status] || styles.pending}`} data-status={status}>
      {statusLabels[status] ?? status}
    </span>
  );
}
