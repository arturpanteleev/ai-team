import { useState, useEffect, useCallback, useRef } from 'react';
import ReactMarkdown from 'react-markdown';
import { useParams, Link } from '../router';
import type { Stage, Approval, CloudRole, DeliveryProjection } from '../types';
import { decideApproval, resumeRun, cancelRun, getActivePrincipal, retryDelivery, getRunTemplateVersion } from '../api';
import { useWebSocket } from '../hooks/useWebSocket';
import { usePageTitle } from '../hooks/usePageTitle';
import { CopyableHash } from '../components/CopyableHash';
import { StatusBadge } from '../components/StatusBadge';
import { StageRow } from '../components/StageRow';
import { StageOwnership } from '../components/StageOwnership';
import { loadPipelineSnapshot, refreshPipelineSnapshot } from '../data/pipelineSnapshot';
import type { PipelineSnapshot } from '../data/pipelineSnapshot';
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
  const [snapshot, setSnapshot] = useState<PipelineSnapshot | null>(null);
  const snapshotRef = useRef<PipelineSnapshot | null>(null);
  const eventQueue = useRef<Promise<void>>(Promise.resolve());
  const [actor, setActor] = useState(principal?.actor_id ?? 'local-user');
  const [answers, setAnswers] = useState<Record<string, string>>({});
  const [returnReasons, setReturnReasons] = useState<Record<string, string>>({});
  const [controlError, setControlError] = useState('');
  const [deliveryRetryError, setDeliveryRetryError] = useState('');
  const [deliveryRetrying, setDeliveryRetrying] = useState(false);
  const [templateVersion, setTemplateVersion] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const run = snapshot?.run ?? null;
  const stages = snapshot?.stages ?? [];
  const artifacts = snapshot?.artifacts ?? [];
  const approvals = snapshot?.approvals ?? [];
  const graph = snapshot?.graph ?? null;
  const nextStage = snapshot?.nextStage ?? '';
  const delivery: DeliveryProjection = snapshot?.delivery ?? { status: 'not_requested' };

  usePageTitle(run ? run.feature : 'Задача');

  const fetchData = useCallback(async () => {
    if (!id) return;
    try {
      const fresh = await loadPipelineSnapshot(Number(id));
      const pinnedTemplate = await getRunTemplateVersion(fresh.run.run_id).catch(() => null);
      snapshotRef.current = fresh;
      setSnapshot(fresh);
      setTemplateVersion(pinnedTemplate?.version ?? '');
      setError(null);
    } catch {
      setError('Не удалось загрузить задачу. Попробуйте обновить страницу.');
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  useWebSocket({
    onEvent: (event) => {
      if (event.run_id !== snapshotRef.current?.run.run_id) return;
      eventQueue.current = eventQueue.current.then(async () => {
        const current = snapshotRef.current;
        if (!current || current.run.run_id !== event.run_id) return;
        const fresh = await refreshPipelineSnapshot(current, event);
        snapshotRef.current = fresh;
        setSnapshot(fresh);
      }).catch(() => setError('Не удалось получить обновление задачи.'));
    },
  });

  useEffect(() => {
    const refreshWhenVisible = () => {
      if (document.visibilityState === 'visible') void fetchData();
    };
    window.addEventListener('focus', refreshWhenVisible);
    document.addEventListener('visibilitychange', refreshWhenVisible);
    return () => {
      window.removeEventListener('focus', refreshWhenVisible);
      document.removeEventListener('visibilitychange', refreshWhenVisible);
    };
  }, [fetchData]);

  // Редкий recovery fallback на случай длительной недоступности WebSocket.
  useEffect(() => {
    if (run?.status !== 'running' && run?.status !== 'waiting_for_approval') return;
    const t = window.setInterval(fetchData, 30000);
    return () => window.clearInterval(t);
  }, [run?.status, fetchData]);

  if (loading) return <div className={styles.loading}>Загружаем задачу…</div>;
  if (error || !run) return <div className={styles.error}>{error || 'Задача не найдена.'}</div>;

  const duration = run.completed_at
          ? ((new Date(run.completed_at).getTime() - new Date(run.started_at).getTime()) / 1000).toFixed(1) + ' с'
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
    if (!run) return;
    setDeliveryRetryError('');
    setDeliveryRetrying(true);
    try {
      await retryDelivery(run.run_id);
      await fetchData();
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Не удалось повторить Git-доставку';
      // The POST may have committed before the connection dropped. Refresh the
      // controller's durable delivery projection before exposing a retry.
      await fetchData();
      setDeliveryRetryError(`${message}. Статус доставки обновлён.`);
    } finally {
      setDeliveryRetrying(false);
    }
  };

  const canRetryDelivery = (!principal || principal.roles.includes('product_owner') || principal.roles.includes('release_manager')) &&
    (run?.status === 'completed' || run?.status === 'completed_with_warnings') &&
    (delivery.status === 'pending' || delivery.status === 'failed');
  const prURL = safePullRequestURL(delivery.record?.pr_url);
  const hasPendingHumanInput = Boolean(nextStage && approvals.some((value) => {
    if (value.status !== 'pending' || value.trigger !== 'human_input' || value.from_stage !== nextStage || value.to_stage !== nextStage) return false;
    const payload = value.payload && typeof value.payload === 'object' ? value.payload as { kind?: unknown; stage_id?: unknown } : null;
    return payload?.kind === 'input' && payload.stage_id === nextStage;
  }));

  return (
    <div className={styles.container}>
      <Link to="/" className={styles.back}>← К задачам</Link>

      <div className={styles.header}>
        <h1 className={styles.title}>{run.feature}</h1>
        <div className={styles.meta}>
          <span className={styles.identifier}>Задача: {run.run_id}</span>
          {templateVersion && <span>Версия шаблона: <code title={templateVersion}>{templateVersion.slice(0, 12)}…{templateVersion.slice(-8)}</code></span>}
          <span>Создана: {new Date(run.started_at).toLocaleString('ru-RU')}</span>
          <span>Длительность: {duration}</span>
          <StatusBadge status={run.status} />
        </div>
      </div>

      {(hasPendingHumanInput || snapshot?.stageOwners[nextStage]) && (
        <StageOwnership
          runId={run.run_id}
          stageId={nextStage}
          actorId={principal?.actor_id ?? actor}
          owner={snapshot?.stageOwners[nextStage]}
          canTake={hasPendingHumanInput}
          onChanged={fetchData}
        />
      )}

      {delivery.status !== 'not_requested' && (
        <section className={styles.workflow} aria-labelledby="git-delivery-heading">
          <h2 id="git-delivery-heading">Git-доставка</h2>
          <p aria-live="polite">Статус: {delivery.status}</p>
          {delivery.record && (
            <div>
              {delivery.record.commit_sha && <p>Коммит: <code>{delivery.record.commit_sha}</code></p>}
              {delivery.record.pr_url && <p>Pull request: {prURL
                ? <a href={prURL} target="_blank" rel="noreferrer">{delivery.record.pr_url}</a>
                : <code>{delivery.record.pr_url}</code>}</p>}
            </div>
          )}
          {delivery.error && <p role="status">Ошибка доставки: {delivery.error}</p>}
          {deliveryRetryError && <p role="alert">Неясный результат повтора: {deliveryRetryError}</p>}
          {canRetryDelivery && (
            <button onClick={sendDeliveryRetry} disabled={deliveryRetrying}>
              {deliveryRetrying ? 'Повторяем доставку…' : 'Повторить одобренную Git-доставку'}
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
              aria-label="Идентификатор участника" placeholder="Идентификатор участника" />}
          {['waiting_for_approval', 'interrupted', 'blocked'].includes(run.status) &&
            <button onClick={() => sendRunCommand('resume')}>Продолжить задачу</button>}
          {['queued', 'running', 'waiting_for_approval'].includes(run.status) &&
            <button onClick={() => sendRunCommand('cancel')}>Отменить задачу</button>}
        </div>
        {controlError && <div className={styles.controlError}>{controlError}</div>}
        {approvals.length === 0 ? <p>Запросов на решение пока нет.</p> : approvals.map((value) => {
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
            <span>{value.status === 'pending' ? 'ожидает решения' : 'решён'} · условие: {value.trigger} · правило голосования: {value.quorum === 'all' ? 'все участники' : 'достаточно одного'}</span>
            <div className={styles.hashes}><span>Хеш решения</span><CopyableHash value={value.subject_hash} label="хеш решения" /></div>
            {value.candidate_sha256 && <div className={styles.hashes}><span>Хеш кандидата</span><CopyableHash value={value.candidate_sha256} label="хеш кандидата" /></div>}
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
                {agreedSpec.brief_version?.sha256 && <div className={styles.hashes}><span>Хеш бизнес-намерения</span><CopyableHash value={agreedSpec.brief_version.sha256} label="хеш бизнес-намерения" /></div>}
                {Object.entries(agreedSpec.artifacts ?? {}).map(([name, digest]) => (
                  <div className={styles.hashes} key={name}><span>{name}</span><CopyableHash value={digest} label={`хеш артефакта ${name}`} /></div>
                ))}
                <div className={styles.questionText}>
                  {artifacts.filter((artifact) => artifact.run_id === run.run_id &&
                    artifact.path.includes(`attempts/${value.attempt_id}/artifacts/`) &&
                    (artifact.path.endsWith('/proposal.md') || artifact.path.endsWith('/spec.md')))
                    .map((artifact) => (
                      <Link key={artifact.path}
                        to={`/artifacts/${encodeURIComponent(run.run_id)}/${artifact.path.split('/').map(encodeURIComponent).join('/')}`}>
                        Открыть {artifact.path.endsWith('/proposal.md') ? 'обоснование' : 'спецификацию продукта'}
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
                {value.artifact_revision_binding_sha256 && <div className={styles.hashes}><span>Хеш привязки версий</span><CopyableHash value={value.artifact_revision_binding_sha256} label="хеш привязки версий" /></div>}
              </details>
            )}
            {value.status === 'pending' && (value.feedback_actions
              ? value.feedback_actions.length > 0
              : value.actions.some((action) => action.startsWith('return_to_'))) && (
              <label className={styles.question}>
                Причина возврата и комментарий для следующей роли
                <textarea value={returnReasons[value.id] ?? ''} maxLength={16 * 1024} rows={4}
                  onChange={(event) => setReturnReasons((current) => ({ ...current, [value.id]: event.target.value }))} />
              </label>
            )}
            {value.payload != null && !question && (
              <details>
              <summary>Данные запроса (canonical JSON)</summary>
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
            <span>Начало: <code>{graph.entry}</code></span>
            {nextStage && <span>Следующий этап: <code>{nextStage}</code></span>}
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
