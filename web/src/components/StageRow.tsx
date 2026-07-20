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
        {stage.verdict && <span className={styles.verdict}>{stage.verdict}</span>}
        <div className={styles.meta}>
          <span className={styles.duration}>{duration}</span>
        </div>
      </div>
      {expanded && (
        <div className={styles.artifacts}>
          <div className={styles.stateGrid}>
            <span>Attempt</span><code>{stage.attempt_id}</code>
            <span>Stage index</span><code>{stage.stage_index}</code>
            <span>Execution</span><code>{stage.execution || '—'}</code>
            <span>Decision</span><code>{stage.decision || '—'}</code>
            <span>Outcome</span><code>{stage.outcome || '—'}</code>
          </div>
          {artifacts.length > 0 ? (
            <>
              <h4>Артефакты</h4>
              {artifacts.map((a) => (
                <Link
                  key={a.path}
                  className={styles.artifactLink}
                  to={`/artifacts/${encodeURIComponent(a.run_id)}/${a.path.split('/').map(encodeURIComponent).join('/')}`}
                >
                  {a.name}
                </Link>
              ))}
            </>
          ) : (
            <span className={styles.empty}>Нет опубликованных артефактов</span>
          )}
        </div>
      )}
      {stage.error && <div className={styles.error}>{stage.error}</div>}
    </>
  );
}
