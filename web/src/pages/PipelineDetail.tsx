import { useState, useEffect, useCallback } from 'react';
import { useParams, Link } from '../router';
import type { PipelineRun, Stage, Artifact, Approval, CloudRole, WorkflowGraph, WorkflowSnapshot } from '../types';
import { getPipelineRun, getPipelineArtifacts, getRunWorkflow, decideApproval, resumeRun, cancelRun, getActivePrincipal } from '../api';
import { useWebSocket } from '../hooks/useWebSocket';
import { StatusBadge } from '../components/StatusBadge';
import { StageRow } from '../components/StageRow';
import styles from './PipelineDetail.module.css';

export function PipelineDetail() {
  const principal = getActivePrincipal();
  const { id } = useParams<{ id: string }>();
  const [run, setRun] = useState<PipelineRun | null>(null);
  const [stages, setStages] = useState<Stage[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [graph, setGraph] = useState<WorkflowGraph | null>(null);
  const [nextStage, setNextStage] = useState('');
  const [actor, setActor] = useState(principal?.actor_id ?? 'local-user');
  const [controlError, setControlError] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchData = useCallback(async () => {
    if (!id) return;
    try {
      const pipelineData = await getPipelineRun(Number(id));
      const [artifactsData, workflowData] = await Promise.all([
        getPipelineArtifacts(Number(id)),
        getRunWorkflow(pipelineData.run.run_id).catch((): WorkflowSnapshot => ({ schema_version: 1 })),
      ]);
      setRun(pipelineData.run);
      setStages(pipelineData.stages);
      setArtifacts(artifactsData);
      setApprovals(pipelineData.approvals ?? []);
      setGraph(workflowData.graph ?? null);
      setNextStage(pipelineData.next_stage ?? '');
    } catch {
      setError('Failed to load pipeline');
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  useWebSocket({
    onEvent: (event) => {
      if (event.run_id === run?.run_id) {
        fetchData();
      }
    },
  });

  // Редкий recovery fallback на случай длительной недоступности WebSocket.
  useEffect(() => {
    if (run?.status !== 'running' && run?.status !== 'waiting_for_approval') return;
    const t = window.setInterval(fetchData, 30000);
    return () => window.clearInterval(t);
  }, [run?.status, fetchData]);

  if (loading) return <div className={styles.loading}>Loading...</div>;
  if (error || !run) return <div className={styles.error}>{error || 'Not found'}</div>;

  const duration = run.completed_at
    ? ((new Date(run.completed_at).getTime() - new Date(run.started_at).getTime()) / 1000).toFixed(1) + 's'
    : '—';

  const getArtifactsForStage = (stage: Stage) =>
    artifacts.filter((a) => a.path.includes(stage.attempt_id) || a.name.toLowerCase().includes(stage.agent_name));

  const sendDecision = async (value: Approval, role: string, action: string) => {
    setControlError('');
    try {
      await decideApproval(run.run_id, value, {
        actor_id: actor, actor_role: role, action,
      });
      await fetchData();
    } catch (err) {
      setControlError(err instanceof Error ? err.message : 'Решение не принято');
      await fetchData();
    }
  };

  const sendRunCommand = async (kind: 'resume' | 'cancel') => {
    setControlError('');
    try {
      if (kind === 'resume') await resumeRun(run.run_id);
      else await cancelRun(run.run_id);
      await fetchData();
    } catch (err) {
      setControlError(err instanceof Error ? err.message : 'Команда не принята');
    }
  };

  return (
    <div className={styles.container}>
      <Link to="/" className={styles.back}>← Назад</Link>

      <div className={styles.header}>
        <h1 className={styles.title}>{run.feature}</h1>
        <div className={styles.meta}>
          <span className={styles.identifier}>Run: {run.run_id}</span>
          <span>Started: {new Date(run.started_at).toLocaleString('ru-RU')}</span>
          <span>Duration: {duration}</span>
          <StatusBadge status={run.status} />
        </div>
      </div>

      <section className={styles.controls}>
        <div className={styles.controlHeader}>
          <h2>Человеческие решения</h2>
          {principal
            ? <span>{principal.actor_id}</span>
            : <input value={actor} onChange={(event) => setActor(event.target.value)}
              aria-label="Actor identity" placeholder="actor identity" />}
          <button onClick={() => sendRunCommand('resume')}>Resume</button>
          <button onClick={() => sendRunCommand('cancel')}>Cancel</button>
        </div>
        {controlError && <div className={styles.controlError}>{controlError}</div>}
        {approvals.length === 0 ? <p>Approvals пока нет.</p> : approvals.map((value) => (
          <article key={value.id} className={styles.approval}>
            <strong>{value.from_stage} → {value.to_stage}</strong>
            <span>{value.status} · trigger {value.trigger} · quorum {value.quorum}</span>
            <code>subject {value.subject_hash}</code>
            {value.candidate_sha256 && <code>candidate {value.candidate_sha256}</code>}
            {value.targets && (
              <small>
                {Object.entries(value.targets).map(([action, target]) => `${action}→${target}`).join(', ')}
              </small>
            )}
            {value.payload != null && (
              <details>
                <summary>Payload (canonical JSON)</summary>
                <pre><code>{JSON.stringify(value.payload, null, 2)}</code></pre>
              </details>
            )}
            <div className={styles.actions}>
              {value.status === 'pending' && value.required_roles
                .filter((role) => !principal || principal.roles.includes(role as CloudRole))
                .flatMap((role) =>
                value.actions.map((action) => (
                  <button key={`${role}:${action}`} onClick={() => sendDecision(value, role, action)}>
                    {action} · {role}
                  </button>
                )))}
            </div>
            {value.decisions?.map((decision) => (
              <small key={`${decision.actor_id}:${decision.actor_role}`}>
                {decision.actor_id} ({decision.actor_role}): {decision.action}
              </small>
            ))}
          </article>
        ))}
      </section>

      {graph && (
        <section className={styles.workflow}>
          <div className={styles.workflowHeader}>
            <h2>Маршрут workflow</h2>
            <span>entry <code>{graph.entry}</code></span>
            {nextStage && <span>next <code>{nextStage}</code></span>}
          </div>
          <div className={styles.nodes}>
            {graph.nodes.map((node) => (
              <span key={node.name} data-current={node.name === nextStage}>
                {node.name}{node.max_visits ? ` · max ${node.max_visits}` : ''}
              </span>
            ))}
          </div>
          <div className={styles.edges}>
            {graph.edges.map((edge) => (
              <article key={`${edge.from}:${edge.outcome}`}>
                <code>{edge.from}</code>
                <strong>{edge.outcome}</strong>
                <code>→ {edge.to}</code>
                {edge.approval && (
                  <small>
                    {edge.approval.roles.join(', ')} · quorum {edge.approval.quorum} ·{' '}
                    {Object.entries(edge.approval.actions).map(([action, target]) => `${action}→${target}`).join(', ')}
                  </small>
                )}
              </article>
            ))}
          </div>
        </section>
      )}

      <div className={styles.stages}>
        {stages.map((stage) => (
          <StageRow
            key={stage.id}
            stage={stage}
            artifacts={getArtifactsForStage(stage)}
            runId={run.run_id}
          />
        ))}
      </div>
    </div>
  );
}
