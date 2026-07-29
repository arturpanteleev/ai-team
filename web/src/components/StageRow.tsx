import { useEffect, useMemo, useState } from 'react';
import { Link } from '../router';
import { getRunLog } from '../api';
import type { Stage, Artifact, CheckEvidence, DeliveryEvidence } from '../types';
import { StatusBadge } from './StatusBadge';
import styles from './StageRow.module.css';

interface StageRowProps {
  stage: Stage;
  artifacts: Artifact[];
  runId?: string;
}

function parseEvidence<T>(value?: string): T | null {
  if (!value) return null;
  try {
    return JSON.parse(value) as T;
  } catch {
    return null;
  }
}

export function StageRow({ stage, artifacts, runId }: StageRowProps) {
  const [expanded, setExpanded] = useState(false);
  const [log, setLog] = useState('');
  const [logTruncated, setLogTruncated] = useState(false);
  const [logError, setLogError] = useState('');
  const checks = useMemo(() => parseEvidence<CheckEvidence[]>(stage.checks_json) ?? [], [stage.checks_json]);
  const mutations = useMemo(() => parseEvidence<string[]>(stage.mutations_json) ?? [], [stage.mutations_json]);
  const delivery = useMemo(() => parseEvidence<DeliveryEvidence>(stage.delivery_json), [stage.delivery_json]);

  useEffect(() => {
    if (!expanded || !runId) return;
    let active = true;
    const read = async () => {
      try {
        const value = await getRunLog(runId, stage.attempt_id);
        if (active) {
          setLog(value.content);
          setLogTruncated(value.truncated);
          setLogError('');
        }
      } catch {
        if (active) setLogError('Лог недоступен');
      }
    };
    void read();
    if (stage.status !== 'running') return () => { active = false; };
    const timer = window.setInterval(read, 1000);
    return () => {
      active = false;
      window.clearInterval(timer);
    };
  }, [expanded, runId, stage.attempt_id, stage.status]);

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
          {checks.length > 0 && (
            <section className={styles.evidence}>
              <h4>Проверки</h4>
              {checks.map((check) => (
                <div key={check.name} className={styles.evidenceRow}>
                  <strong>{check.name}</strong>
                  <span>{check.class} · {check.policy}</span>
                  <code>{check.command.join(' ')}</code>
                  <span>{check.status} · exit {check.exit_code}{check.reason ? ` · ${check.reason}` : ''}</span>
                </div>
              ))}
            </section>
          )}
          {mutations.length > 0 && (
            <section className={styles.evidence}>
              <h4>Мутации</h4>
              {mutations.map((mutation) => <code key={mutation}>{mutation}</code>)}
            </section>
          )}
          {delivery && (
            <section className={styles.evidence}>
              <h4>Delivery</h4>
              {delivery.pr_url && <a href={delivery.pr_url}>{delivery.pr_url}</a>}
              {delivery.steps?.map((step) => (
                <div key={step.step} className={styles.evidenceRow}>
                  <strong>{step.step}</strong>
                  <code>{step.command?.join(' ') || '—'}</code>
                  <span>{step.status} · exit {step.exit_code}{step.reason ? ` · ${step.reason}` : ''}</span>
                </div>
              ))}
            </section>
          )}
          {runId && (
            <section className={styles.evidence}>
              <h4>Лог attempt</h4>
              {logTruncated && <span className={styles.empty}>Показан хвост последних 64 KiB.</span>}
              {logError ? <span className={styles.empty}>{logError}</span> :
                <pre className={styles.log}>{log || 'Лог пока пуст.'}</pre>}
            </section>
          )}
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
