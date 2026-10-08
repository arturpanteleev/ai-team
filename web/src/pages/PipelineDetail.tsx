import { useState, useEffect, useCallback } from 'react';
import ReactMarkdown from 'react-markdown';
import { useParams, Link } from '../router';
import type { PipelineRun, Stage, Artifact, Approval, CloudRole, DeliveryProjection, WorkflowGraph, WorkflowSnapshot } from '../types';
import { getPipelineRun, getPipelineArtifacts, getRunWorkflow, decideApproval, resumeRun, retryDelivery, cancelRun, getActivePrincipal } from '../api';
import { useWebSocket } from '../hooks/useWebSocket';
import { StatusBadge } from '../components/StatusBadge';
import { StageRow } from '../components/StageRow';
import styles from './PipelineDetail.module.css';

function safePullRequestURL(value?: string): string | null {
  if (!value) return null;
  try {
    const url = new URL(value);
    return url.protocol === 'https:' && !url.username && !url.password ? url.toString() : null;
  } catch {
    return null;
  }
}

export function PipelineDetail() {
  const principal = getActivePrincipal();
  const { id } = useParams<{ id: string }>();
  const [run, setRun] = useState<PipelineRun | null>(null);
  const [stages, setStages] = useState<Stage[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [delivery, setDelivery] = useState<DeliveryProjection>({ status: 'not_requested' });
  const [deliveryRetryError, setDeliveryRetryError] = useState('');
  const [deliveryRetrying, setDeliveryRetrying] = useState(false);
  const [graph, setGraph] = useState<WorkflowGraph | null>(null);
  const [nextStage, setNextStage] = useState('');
  const [actor, setActor] = useState(principal?.actor_id ?? 'local-user');
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const [returnReasons, setReturnReasons] = useState<Record<string, string>>({});
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
      setDelivery(pipelineData.delivery ?? { status: 'not_requested' });
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

  const sendDecision = async (value: Approval, role: string, action: string, comment?: string) => {
    setControlError('');
    try {
      await decideApproval(run.run_id, value, {
        actor_id: actor, actor_role: role, action, comment,
        ...(value.artifact_revisions && Object.keys(value.artifact_revisions).length > 0
          ? { artifact_revisions: value.artifact_revisions } : {}),
      });
      await fetchData();
    } catch (err) {
      setControlError(err instanceof Error ? err.message : 'Решение не принято');
      await fetchData();
    }
  };

  const requiresReturnReason = (value: Approval, action: string) => value.feedback_actions
    ? value.feedback_actions.includes(action)
    : action.startsWith('return_to_');

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

  const sendDeliveryRetry = async () => {
    setDeliveryRetryError('');
    setDeliveryRetrying(true);
    try {
      await retryDelivery(run.run_id);
      await fetchData();
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Git delivery retry failed';
      // The controller may have completed the POST before the connection was
      // lost. Re-read its durable receipt/event projection before reporting the
      // outcome so the UI does not offer a duplicate delivery blindly.
      await fetchData();
      setDeliveryRetryError(`${message}. Delivery status was refreshed from the controller.`);
    } finally {
      setDeliveryRetrying(false);
    }
  };

  const canRetryDelivery = (!principal || principal.roles.includes('product_owner') || principal.roles.includes('release_manager')) &&
    (run.status === 'completed' || run.status === 'completed_with_warnings') &&
    (delivery.status === 'pending' || delivery.status === 'failed');
  const deliveryStatus = delivery.status;
  const prURL = safePullRequestURL(delivery.record?.pr_url);

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

      {deliveryStatus !== 'not_requested' && (
        <section className={styles.workflow} aria-labelledby="git-delivery-heading">
          <h2 id="git-delivery-heading">Git delivery</h2>
          <p aria-live="polite">Status: {deliveryStatus}</p>
          {delivery.record && (
            <div>
              {delivery.record.commit_sha && <p>Commit: <code>{delivery.record.commit_sha}</code></p>}
              {delivery.record.pr_url && <p>Pull request: {prURL
                ? <a href={prURL} target="_blank" rel="noreferrer">{delivery.record.pr_url}</a>
                : <code>{delivery.record.pr_url}</code>}</p>}
            </div>
          )}
          {delivery.error && <p role="status">Delivery failed: {delivery.error}</p>}
          {deliveryRetryError && <p role="alert">Delivery retry response was unclear: {deliveryRetryError}</p>}
          {canRetryDelivery && (
            <button onClick={sendDeliveryRetry} disabled={deliveryRetrying}>
              {deliveryRetrying ? 'Retrying delivery…' : 'Retry approved Git delivery'}
            </button>
          )}
        </section>
      )}

      {artifacts.some((artifact) => artifact.run_id === run.run_id && artifact.path.startsWith('brief/')) && (
        <section className={styles.workflow}>
          <h2>История бизнес-намерения</h2>
          <div className={styles.edges}>
            {artifacts.filter((artifact) => artifact.run_id === run.run_id && artifact.path.startsWith('brief/'))
              .sort((left, right) => left.path.localeCompare(right.path))
              .map((artifact) => (
                <Link key={artifact.path}
                  to={`/artifacts/${encodeURIComponent(run.run_id)}/${artifact.path.split('/').map(encodeURIComponent).join('/')}`}>
                  {artifact.path.replace('brief/', '')}
                </Link>
              ))}
          </div>
        </section>
      )}

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
        {approvals.length === 0 ? <p>Approvals пока нет.</p> : approvals.map((value) => {
          const question = value.payload && typeof value.payload === 'object' &&
            (value.payload as { kind?: unknown }).kind === 'questions'
            ? value.payload as { kind: 'questions'; markdown?: string }
            : null;
          const agreedSpec = value.payload && typeof value.payload === 'object' &&
            (value.payload as { kind?: unknown }).kind === 'agreed_spec'
            ? value.payload as { kind: 'agreed_spec'; brief_version?: { id?: string; sha256?: string }; artifacts?: Record<string, string> }
            : null;
          return (
          <article key={value.id} className={styles.approval}>
            <strong>{value.from_stage} → {value.to_stage}</strong>
            <span>{value.status} · trigger {value.trigger} · quorum {value.quorum}</span>
            <code>subject {value.subject_hash}</code>
            {value.candidate_sha256 && <code>candidate {value.candidate_sha256}</code>}
            {question && (
              <div className={styles.question}>
                <h3>Вопрос аналитика</h3>
                <div className={styles.questionText}><ReactMarkdown>{question.markdown || 'Текст вопроса находится в артефакте этапа.'}</ReactMarkdown></div>
                {value.status === 'pending' && (
                  <label>
                    Ваш ответ
                    <textarea value={answers[value.id] ?? ''}
                      onChange={(event) => setAnswers((current) => ({ ...current, [value.id]: event.target.value }))}
                      maxLength={16 * 1024} rows={5} />
                  </label>
                )}
              </div>
            )}
            {agreedSpec && (
              <div className={styles.question}>
                <h3>Product Owner согласует требования перед архитектором</h3>
                <p>Версия намерения: {agreedSpec.brief_version?.id ?? 'не указана'}</p>
                <code>brief SHA-256: {agreedSpec.brief_version?.sha256 ?? 'не указан'}</code>
                {Object.entries(agreedSpec.artifacts ?? {}).map(([name, digest]) => (
                  <code key={name}>{name} SHA-256: {digest}</code>
                ))}
                <div className={styles.questionText}>
                  {artifacts.filter((artifact) => artifact.run_id === run.run_id &&
                    artifact.path.includes(`attempts/${value.attempt_id}/artifacts/`) &&
                    (artifact.path.endsWith('/proposal.md') || artifact.path.endsWith('/spec.md')))
                    .map((artifact) => (
                      <Link key={artifact.path}
                        to={`/artifacts/${encodeURIComponent(run.run_id)}/${artifact.path.split('/').map(encodeURIComponent).join('/')}`}>
                        Открыть {artifact.path.endsWith('/proposal.md') ? 'обоснование' : 'product spec'}
                      </Link>
                    ))}
                </div>
              </div>
            )}
            {value.targets && (
              <small>
                {Object.entries(value.targets).map(([action, target]) => `${action}→${target}`).join(', ')}
              </small>
            )}
            {value.artifact_revisions && Object.keys(value.artifact_revisions).length > 0 && (
              <details>
                <summary>{value.status === 'pending' && value.quorum === 'all'
                  ? 'Закреплённые версии для всех голосов' : 'Закреплённые версии артефактов'}</summary>
                {value.status === 'pending' && value.quorum === 'all' && (
                  <small>Первый голос закрепил эти версии. Остальные участники отправят тот же выбор, даже если добавлена более новая версия.</small>
                )}
                {Object.entries(value.artifact_revisions).map(([path, revision]) => (
                  <code key={path}>{path} · {revision}</code>
                ))}
                {value.artifact_revision_binding_sha256 && <code>binding SHA-256: {value.artifact_revision_binding_sha256}</code>}
              </details>
            )}
            {value.status === 'pending' && (value.feedback_actions
              ? value.feedback_actions.length > 0
              : value.actions.some((action) => action.startsWith('return_to_'))) && (
              <label className={styles.question}>
                Причина возврата и feedback для следующей роли
                <textarea value={returnReasons[value.id] ?? ''} maxLength={16 * 1024} rows={4}
                  onChange={(event) => setReturnReasons((current) => ({ ...current, [value.id]: event.target.value }))} />
              </label>
            )}
            {value.payload != null && !question && (
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
                  <button key={`${role}:${action}`}
                    disabled={Boolean(question && action === 'answer_questions' && !(answers[value.id] ?? '').trim()) ||
                      Boolean(requiresReturnReason(value, action) && !(returnReasons[value.id] ?? '').trim())}
                    onClick={() => sendDecision(value, role, action, action === 'answer_questions' ? answers[value.id]
                      : requiresReturnReason(value, action) ? returnReasons[value.id] : undefined)}>
                    {question && action === 'answer_questions' ? 'Ответить и продолжить'
                      : agreedSpec && action === 'approve_spec' ? 'Согласовать ТЗ и передать архитектору'
                        : action === 'reject' || action === 'stop' ? 'Отклонить / остановить' : `${action} · ${role}`}
                  </button>
                )))}
            </div>
            {value.decisions?.map((decision) => (
              <small key={`${decision.actor_id}:${decision.actor_role}`}>
                {decision.actor_id} ({decision.actor_role}): {decision.action}
              </small>
            ))}
          </article>
          );
        })}
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
