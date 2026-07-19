import { useState } from 'react';
import { Link } from 'react-router-dom';
import type { Stage, Artifact } from '../types';
import { StatusBadge } from './StatusBadge';
import styles from './StageRow.module.css';

interface StageRowProps {
  stage: Stage;
  artifacts: Artifact[];
}

export function StageRow({ stage, artifacts }: StageRowProps) {
  const [expanded, setExpanded] = useState(false);

  const duration = stage.duration_ms
    ? (stage.duration_ms / 1000).toFixed(1) + 's'
    : '—';

  return (
    <>
      <div
        className={`${styles.row} ${expanded ? styles.expanded : ''}`}
        onClick={() => setExpanded(!expanded)}
      >
        <span className={styles.agent}>{stage.agent_name}</span>
        <StatusBadge status={stage.status} />
        <div className={styles.meta}>
          <span className={styles.duration}>{duration}</span>
        </div>
      </div>
      {expanded && artifacts.length > 0 && (
        <div className={styles.artifacts}>
          <h4>Артефакты</h4>
          {artifacts.map((a) => (
            <Link
              key={a.path}
              className={styles.artifactLink}
              to={`/artifacts/${encodeURIComponent(a.path)}`}
            >
              {a.name}
            </Link>
          ))}
        </div>
      )}
      {stage.error && <div className={styles.error}>{stage.error}</div>}
    </>
  );
}