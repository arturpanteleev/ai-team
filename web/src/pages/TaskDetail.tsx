import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import ReactMarkdown from 'react-markdown';
import { Link, useParams } from '../router';
import {
  answerTaskGrill,
  approveTaskDeliveryPlan,
  cancelRun,
  confirmTaskStage,
  finishTaskGrill,
  getPipelineArtifacts,
  getTaskRunDetails,
  getTaskRunEvents,
  overrideTaskStageCheck,
  returnTaskStage,
  runTaskStageAgent,
  setTaskStageExecutor,
  skipTaskStage,
  submitTaskStageResult,
  takeTaskStage,
} from '../api';
import type { Approval, Artifact, Stage, TaskEvent, TaskResultRevision, TaskRunDetails } from '../types';
import { useWebSocket } from '../hooks/useWebSocket';
import { usePageTitle } from '../hooks/usePageTitle';
import { CopyableHash } from '../components/CopyableHash';
import { StageResultForm } from '../components/StageResultForm';
import type { StageResultSubmission } from '../stageResultValidation';
import { StageRow } from '../components/StageRow';
import { StatusBadge } from '../components/StatusBadge';
import { TaskTimeline } from '../components/TaskTimeline';
import { clearStageResultDraft, loadStageResultDraft } from '../stageResultDraft';
import styles from './TaskDetail.module.css';

interface TaskDetailSnapshot {
  detail: TaskRunDetails;
  artifacts: Artifact[];
  events: TaskEvent[];
}

async function loadRunEvents(runId: string): Promise<TaskEvent[]> {
  const events: TaskEvent[] = [];
  let cursor = 0;
  for (let page = 0; page < 1000; page += 1) {
    const response = await getTaskRunEvents(runId, cursor, 1000);
    events.push(...response.events);
    if (response.events.length < 1000 || response.next_cursor <= cursor) break;
    cursor = response.next_cursor;
  }
  return events;
}

function httpLink(value: string): boolean {
  try {
    const url = new URL(value);
    return (url.protocol === 'https:' || url.protocol === 'http:') && !url.username && !url.password;
  } catch {
    return false;
  }
}

function eventTitle(type: string): string {
  const titles: Record<string, string> = {
    run_started: 'Задача создана', run_resumed: 'Задача продолжена', run_paused: 'Задача приостановлена',
    run_canceled: 'Задача отменена', run_finished: 'Задача завершена', attempt_started: 'Этап начат',
    attempt_finished: 'Этап завершён', attempts_invalidated: 'Результаты этапа пересчитаны',
    approval_requested: 'Запрошено решение', approval_decided: 'Решение принято',
    transition_selected: 'Выбран переход', stage_taken: 'Этап взят участником', stage_agent_requested: 'Участник запустил агента',
    executor_changed: 'Сменён исполнитель этапа', stage_skipped: 'Этап пропущен',
    stage_returned: 'Выполнен возврат', stage_stalled: 'Этап остановился', check_overridden: 'Проверка обойдена',
    integration_error: 'Ошибка интеграции', description_missing: 'Нет описания результата',
  };
  return titles[type] ?? type;
}

function currentApproval(details: TaskRunDetails): Approval | undefined {
  const pendingID = details.state?.pending_approval_id;
  return details.approvals.find((approval) => approval.id === pendingID && approval.status === 'pending');
}

export function TaskDetail() {
  const { runID = '' } = useParams<{ runID: string }>();
  const [snapshot, setSnapshot] = useState<TaskDetailSnapshot | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [actionError, setActionError] = useState('');
  const [actionStatus, setActionStatus] = useState('');
  const [working, setWorking] = useState(false);
  const [showResultForm, setShowResultForm] = useState(false);
  const [skipReason, setSkipReason] = useState('');
  const [overrideReason, setOverrideReason] = useState('');
  const [returnReason, setReturnReason] = useState('');
  const [returnTarget, setReturnTarget] = useState('');
  const [grillAnswer, setGrillAnswer] = useState('');
  const eventQueue = useRef<Promise<void>>(Promise.resolve());
  const snapshotRef = useRef<TaskDetailSnapshot | null>(null);
  const activeRunID = useRef(runID);
  const refreshGeneration = useRef(0);
  activeRunID.current = runID;

  const refresh = useCallback(async () => {
    const requestedRunID = runID;
    if (!requestedRunID || activeRunID.current !== requestedRunID) return;
    const generation = ++refreshGeneration.current;
    const isCurrent = () => activeRunID.current === requestedRunID && refreshGeneration.current === generation;
    try {
      const details = await getTaskRunDetails(requestedRunID);
      if (!isCurrent()) return;
      if (details.run.run_id !== requestedRunID) throw new Error('Сервер вернул данные другой задачи.');
      const [artifacts, events] = await Promise.all([
        getPipelineArtifacts(details.run.id),
        loadRunEvents(requestedRunID),
      ]);
      if (!isCurrent()) return;
      const next = { detail: details, artifacts, events };
      snapshotRef.current = next;
      setSnapshot(next);
      setError('');
    } catch (reason) {
      if (isCurrent()) throw reason;
    }
  }, [runID]);

  useEffect(() => {
    let active = true;
    snapshotRef.current = null;
    setSnapshot(null);
    setLoading(true);
    setError('');
    setActionError('');
    setActionStatus('');
    setWorking(false);
    setShowResultForm(false);
    setSkipReason('');
    setOverrideReason('');
    setReturnReason('');
    setReturnTarget('');
    setGrillAnswer('');
    refresh()
      .catch((reason: unknown) => {
        if (active) setError(reason instanceof Error ? reason.message : 'Не удалось загрузить задачу.');
      })
      .finally(() => { if (active) setLoading(false); });
    return () => { active = false; refreshGeneration.current += 1; };
  }, [refresh, runID]);

  const visibleSnapshot = snapshot?.detail.run.run_id === runID ? snapshot : null;
  const details = visibleSnapshot?.detail;
  const run = details?.run;
  usePageTitle(run?.feature ?? 'Задача');

  useWebSocket({
    onEvent: (event) => {
      if (event.run_id !== runID) return;
      eventQueue.current = eventQueue.current.then(async () => {
        if (snapshotRef.current?.detail.run.run_id !== runID) return;
        await refresh();
      }).catch(() => {
        if (activeRunID.current === runID) setError('Не удалось получить обновление задачи.');
      });
    },
  });

  useEffect(() => {
    const runStatus = run?.status;
    if (!runStatus || !['queued', 'running', 'waiting_for_approval'].includes(runStatus)) return;
    let active = true;
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') void refresh().catch(() => {
        if (active && activeRunID.current === runID) setError('Не удалось обновить задачу.');
      });
    }, 30000);
    return () => { active = false; window.clearInterval(timer); };
  }, [run?.status, refresh, runID]);

  const template = details?.template;
  const currentStageID = details?.state?.next_stage ?? '';
  const currentStage = template?.stages.find((stage) => stage.id === currentStageID);
  const currentExecutor = currentStage
    ? details?.state?.executor_overrides?.[currentStage.id]?.executor ?? currentStage.executor
    : 'human';
  const allowed = useMemo(() => new Set(details?.allowed_actions ?? []), [details?.allowed_actions]);
  const pending = details ? currentApproval(details) : undefined;
  const resultsAllowed = allowed.has('results');
  const currentRunID = run?.run_id;

  // A draft saved before a page reload reopens the result form for that stage.
  useEffect(() => {
    if (currentRunID && currentStageID && resultsAllowed && loadStageResultDraft(currentRunID, currentStageID)) {
      setShowResultForm(true);
    }
  }, [currentRunID, currentStageID, resultsAllowed]);
  const returnOptions = template?.returns.filter((route) => route.from === currentStageID) ?? [];
  const duration = run?.completed_at
    ? Math.max(0, new Date(run.completed_at).getTime() - new Date(run.started_at).getTime())
    : null;

  const perform = async (label: string, action: () => Promise<unknown>): Promise<boolean> => {
    const actionRunID = runID;
    const actionStillTargetsCurrentRun = () => activeRunID.current === actionRunID;
    setActionError('');
    setActionStatus(`${label}…`);
    setWorking(true);
    try {
      await action();
      if (!actionStillTargetsCurrentRun()) return false;
      await refresh();
      if (!actionStillTargetsCurrentRun()) return false;
      setActionStatus(`${label} выполнено.`);
      return true;
    } catch (reason) {
      if (!actionStillTargetsCurrentRun()) return false;
      setActionStatus('');
      setActionError(reason instanceof Error ? reason.message : `Не удалось выполнить действие: ${label.toLowerCase()}.`);
      await refresh().catch(() => undefined);
      return false;
    } finally {
      if (actionStillTargetsCurrentRun()) setWorking(false);
    }
  };

  const submitResult = async (value: StageResultSubmission) => {
    if (!run || !currentStage) return;
    const stageRunID = run.run_id;
    const stageID = currentStage.id;
    // The draft is dropped only once the server accepted the result; a
    // rejected submission keeps it for the next attempt.
    const succeeded = await perform('Результат отправлен', async () => {
      await submitTaskStageResult(stageRunID, stageID, value);
      clearStageResultDraft(stageRunID, stageID);
    });
    if (succeeded) setShowResultForm(false);
  };

  const submitReason = (action: 'skip' | 'override' | 'return') => async (event: FormEvent) => {
    event.preventDefault();
    if (!run || !currentStage) return;
    if (action === 'skip') {
      if (await perform('Этап пропущен', () => skipTaskStage(run.run_id, currentStage.id, skipReason.trim()))) setSkipReason('');
    } else if (action === 'override') {
      if (await perform('Проверка обойдена', () => overrideTaskStageCheck(run.run_id, currentStage.id, overrideReason.trim()))) setOverrideReason('');
    } else {
      if (await perform('Возврат отправлен', () => returnTaskStage(run.run_id, currentStage.id, returnTarget, returnReason.trim()))) setReturnReason('');
    }
  };

  if (loading) return <main className={styles.state}>Загружаем задачу…</main>;
  if (error || !visibleSnapshot || !details || !run) {
    if (!error && !visibleSnapshot) return <main className={styles.state}>Загружаем задачу…</main>;
    return <main className={styles.state} role="alert">{error || 'Задача не найдена.'}</main>;
  }

  const runCancelable = ['queued', 'running', 'waiting_for_approval'].includes(run.status);
  const attemptArtifacts = (stage: Stage) => visibleSnapshot.artifacts.filter((artifact) =>
    artifact.path.includes(`attempts/${stage.attempt_id}/`));

  return (
    <main className={styles.container}>
      <Link to="/tasks" className={styles.back}>← К задачам</Link>
      <header className={styles.header}>
        <div className={styles.headingLine}>
          <div>
            <h1>{run.feature}</h1>
            {details.task && <p className={styles.task}>{details.task}</p>}
          </div>
          <StatusBadge status={run.status} />
        </div>
        <div className={styles.meta}>
          <span><CopyableHash value={run.run_id} label="ID задачи" /></span>
          <span>Режим: {details.mode === 'auto' ? 'Авто' : 'Вручную'}</span>
          <span>Создана: {new Date(run.started_at).toLocaleString('ru-RU')}</span>
          {duration !== null && <span>Длительность: {(duration / 1000).toFixed(1)} с</span>}
          {template && <span>Шаблон: {template.title || template.template} · <code>{template.version.slice(0, 12)}…</code></span>}
        </div>
        {(details.external_links ?? []).length > 0 && (
          <ul className={styles.externalLinks} aria-label="Внешние ссылки">
            {details.external_links?.map((value) => (
              <li key={value}>{httpLink(value)
                ? <a href={value} target="_blank" rel="noreferrer">{value}</a>
                : <code>{value}</code>}</li>
            ))}
          </ul>
        )}
      </header>

      <section className={styles.panel} aria-labelledby="current-stage-heading">
        <div className={styles.panelHeading}>
          <div>
            <p className={styles.eyebrow}>Текущий этап</p>
            <h2 id="current-stage-heading">{currentStage?.title ?? currentStageID ?? 'Этапов нет'}</h2>
            {currentStage && <p>{currentStage.function} · {currentExecutor === 'agent'
              ? `агент ${currentStage.agent ?? ''}` : 'участник'} · результат: {currentStage.result}</p>}
            {details.state && <p className={styles.phase}>Фаза: {details.state.phase}</p>}
            {details.stage_owners[currentStageID] && <p>Ответственный: {details.stage_owners[currentStageID].actor_id}</p>}
          </div>
          {runCancelable && <button type="button" disabled={working}
            onClick={() => void perform('Задача отменена', () => cancelRun(run.run_id))}>Отменить задачу</button>}
        </div>

        {actionStatus && <p className={styles.actionStatus} role="status">{actionStatus}</p>}
        {actionError && <p className={styles.actionError} role="alert">{actionError}</p>}
        {currentStage && allowed.size > 0 && <div className={styles.actions}>
          {allowed.has('take') && <button type="button" disabled={working}
            onClick={() => void perform('Этап взят', () => takeTaskStage(run.run_id, currentStage.id))}>Взять этап</button>}
          {allowed.has('run-agent') && <button type="button" disabled={working}
            onClick={() => void perform('Агент запущен', () => runTaskStageAgent(run.run_id, currentStage.id,
              pending?.actions.includes('retry') ? 'refine' : 'run'))}>
            {pending?.actions.includes('retry') ? 'Попросить агента доработать' : 'Сделай: запустить агента'}
          </button>}
          {allowed.has('executor') && currentStage.agent && (
            <label className={styles.inlineSelect}>Исполнитель
              <select disabled={working} value={currentExecutor}
                onChange={(event) => void perform('Исполнитель изменён', () => setTaskStageExecutor(
                  run.run_id, currentStage.id, event.currentTarget.value as 'human' | 'agent'))}>
                <option value="human">Участник</option><option value="agent">Агент: {currentStage.agent}</option>
              </select>
            </label>
          )}
          {allowed.has('results') && !showResultForm && <button type="button" disabled={working}
            onClick={() => setShowResultForm(true)}>Сдать результат</button>}
          {allowed.has('confirm') && <button type="button" disabled={working}
            onClick={() => void perform('Этап подтверждён', () => confirmTaskStage(run.run_id, currentStage.id))}>Подтвердить этап</button>}
          {allowed.has('skip') && <form className={styles.reasonForm} onSubmit={submitReason('skip')}>
            <label>Причина пропуска
              <input value={skipReason} onChange={(event) => setSkipReason(event.target.value)} required maxLength={4000} />
            </label>
            <button disabled={working || !skipReason.trim()}>Пропустить этап</button>
          </form>}
          {allowed.has('override') && <form className={styles.reasonForm} onSubmit={submitReason('override')}>
            <label>Причина обхода проверки
              <input value={overrideReason} onChange={(event) => setOverrideReason(event.target.value)} required maxLength={4000} />
            </label>
            <button disabled={working || !overrideReason.trim()}>Обойти проверку</button>
          </form>}
          {allowed.has('return') && returnOptions.length > 0 && <form className={styles.reasonForm} onSubmit={submitReason('return')}>
            <label>Вернуть на этап
              <select value={returnTarget || returnOptions[0].to} onChange={(event) => setReturnTarget(event.target.value)}>
                {returnOptions.map((route) => <option key={route.to} value={route.to}>
                  {template?.stages.find((stage) => stage.id === route.to)?.title ?? route.to}
                </option>)}
              </select>
            </label>
            <label>Причина возврата
              <input value={returnReason} onChange={(event) => setReturnReason(event.target.value)} required maxLength={4000} />
            </label>
            <button disabled={working || !returnReason.trim()}>Вернуть этап</button>
          </form>}
          {(allowed.has('grill/answer') || allowed.has('grill/done')) && pending && <GrillPanel approval={pending} answer={grillAnswer}
            setAnswer={setGrillAnswer} disabled={working}
            canAnswer={allowed.has('grill/answer')} canDone={allowed.has('grill/done')}
            onAnswer={() => void perform('Ответ отправлен', () => answerTaskGrill(run.run_id, currentStage.id, grillAnswer.trim()))}
            onDone={() => void perform('Грилл завершён', () => finishTaskGrill(run.run_id, currentStage.id))} />}
          {allowed.has('plan/approve') && pending?.trigger === 'delivery_plan' && <div className={styles.planApproval}>
            <p>План доставки ожидает подтверждения. Хеш: <code>{pending.subject_hash}</code></p>
            <DeliveryPlanPayload payload={pending.payload} />
            <button type="button" disabled={working} onClick={() => void perform('План подтверждён', () =>
              approveTaskDeliveryPlan(run.run_id, pending.subject_hash))}>Подтвердить план</button>
          </div>}
        </div>}

        {currentStage && showResultForm && allowed.has('results') && (
          <StageResultForm key={`${run.run_id}:${currentStage.id}`} runId={run.run_id} stage={currentStage}
            working={working} onSubmit={(value) => void submitResult(value)} onCancel={() => setShowResultForm(false)} />
        )}
      </section>

      <section className={styles.panel} aria-labelledby="timeline-heading">
        <h2 id="timeline-heading">Таймлайн задачи</h2>
        <TaskTimeline details={details} events={visibleSnapshot.events} eventTitle={eventTitle} />
      </section>

      <section className={styles.panel} aria-labelledby="results-heading">
        <h2 id="results-heading">Результаты этапов и версии</h2>
        <div className={styles.resultStages}>
          {(template?.stages ?? []).map((stage) => (
            <ResultHistory key={stage.id} title={stage.title} stageID={stage.id} revisions={details.results[stage.id] ?? []} />
          ))}
        </div>
      </section>

      <section className={styles.panel} aria-labelledby="grill-history-heading">
        <h2 id="grill-history-heading">История грилла</h2>
        <GrillHistory approvals={details.approvals.filter((approval) =>
          approval.payload && typeof approval.payload === 'object' &&
          typeof (approval.payload as { markdown?: unknown }).markdown === 'string' && approval.status !== 'pending')}
        />
      </section>

      <section className={styles.panel} aria-labelledby="attempts-heading">
        <h2 id="attempts-heading">Этапы, проверки и логи</h2>
        {details.stages.length === 0 ? <p>Попыток пока нет.</p> : <div className={styles.attempts}>
          {details.stages.map((stage) => {
            const title = template?.stages.find((item) => item.id === stage.agent_name)?.title ?? stage.agent_name;
            return <article key={stage.id} className={styles.attempt}>
              <h3>{title} · попытка {stage.stage_index}</h3>
              <div className={styles.attemptMeta}><code>{stage.attempt_id}</code><StatusBadge status={stage.status} />
                <a href={`/api/runs/${encodeURIComponent(run.run_id)}/logs/${encodeURIComponent(stage.attempt_id)}`}
                  target="_blank" rel="noreferrer">Открыть лог этапа</a>
              </div>
              <StageRow stage={stage} artifacts={attemptArtifacts(stage)} runId={run.run_id} />
            </article>;
          })}
        </div>}
      </section>

      <section className={styles.panel} aria-labelledby="events-heading">
        <div className={styles.panelHeading}>
          <div><h2 id="events-heading">Лента событий задачи</h2><p>Обновляется по событиям выполнения.</p></div>
        </div>
        {visibleSnapshot.events.length === 0 ? <p>Событий пока нет.</p> : <ol className={styles.events}>
          {visibleSnapshot.events.map((event) => <li key={event.cursor} id={`task-event-${event.cursor}`} data-severity={event.severity}>
            <time dateTime={event.timestamp}>{new Date(event.timestamp).toLocaleString('ru-RU')}</time>
            <strong>{eventTitle(event.type)}</strong>
            <span>{[event.stage, event.actor, event.executor, event.attempt_id].filter(Boolean).join(' · ')}</span>
            {event.severity === 'warning' && <span className={styles.warning}>Предупреждение</span>}
            {Object.keys(event.data).length > 0 && <details>
              <summary>Подробности</summary><pre>{JSON.stringify(event.data, null, 2)}</pre>
            </details>}
          </li>)}
        </ol>}
      </section>
    </main>
  );
}

function GrillPanel({ approval, answer, setAnswer, disabled, canAnswer, canDone, onAnswer, onDone }: {
  approval: Approval;
  answer: string;
  setAnswer: (value: string) => void;
  disabled: boolean;
  canAnswer: boolean;
  canDone: boolean;
  onAnswer: () => void;
  onDone: () => void;
}) {
  const payload = approval.payload && typeof approval.payload === 'object'
    ? approval.payload as { markdown?: unknown; kind?: unknown } : null;
  const markdown = typeof payload?.markdown === 'string' ? payload.markdown : '';
  return <section className={styles.grill} aria-labelledby="grill-heading">
    <h3 id="grill-heading">Грилл: вопросы и ответы</h3>
    {markdown ? <div className={styles.markdown}><ReactMarkdown>{markdown}</ReactMarkdown></div>
      : <p>Вопросы доступны в данных запроса.</p>}
    {approval.decisions?.map((decision, index) => <article key={`${decision.actor_id}-${index}`} className={styles.answer}>
      <strong>{decision.actor_id} · {decision.action}</strong>
      {decision.comment && <p>{decision.comment}</p>}
    </article>)}
    {canAnswer && <label>Ваш ответ
      <textarea value={answer} onChange={(event) => setAnswer(event.target.value)} rows={4} maxLength={16 * 1024} />
    </label>}
    <div className={styles.actions}>
      {canAnswer && <button type="button" disabled={disabled || !answer.trim()} onClick={onAnswer}>Ответить и продолжить</button>}
      {canDone && <button type="button" disabled={disabled} onClick={onDone}>Завершить грилл без ответа</button>}
    </div>
  </section>;
}

function GrillHistory({ approvals }: { approvals: Approval[] }) {
  if (approvals.length === 0) return <p>Завершённых вопросов пока нет.</p>;
  return <div className={styles.grillHistory}>
    {approvals.map((approval) => {
      const payload = approval.payload as { markdown?: string };
      return <article key={approval.id}>
        <div className={styles.versionHeading}>
          <strong>{approval.from_stage}</strong>
          <span>{approval.resolved_action ?? 'решение принято'}</span>
          {approval.resolved_at && <time dateTime={approval.resolved_at}>{new Date(approval.resolved_at).toLocaleString('ru-RU')}</time>}
        </div>
        {payload.markdown && <div className={styles.markdown}><ReactMarkdown>{payload.markdown}</ReactMarkdown></div>}
        {approval.decisions?.map((decision, index) => <div key={`${decision.actor_id}-${index}`} className={styles.answer}>
          <strong>{decision.actor_id} · {decision.action}</strong>
          {decision.comment && <p>{decision.comment}</p>}
        </div>)}
      </article>;
    })}
  </div>;
}

function ResultHistory({ title, stageID, revisions }: {
  title: string;
  stageID: string;
  revisions: TaskResultRevision[];
}) {
  const previous = revisions.length > 1 ? revisions[revisions.length - 2] : null;
  const latest = revisions.at(-1);
  return <article className={styles.resultStage}>
    <h3>{title} <code>{stageID}</code></h3>
    {revisions.length === 0 ? <p>Версий результата пока нет.</p> : <>
      <ol className={styles.versions}>
        {revisions.map((revision) => <li key={revision.id}>
          <div className={styles.versionHeading}>
            <strong>Версия {revision.revision}</strong>
            <span>{revision.actor_id} · {new Date(revision.created_at).toLocaleString('ru-RU')}</span>
            <CopyableHash value={revision.sha256} label={`хеш версии ${revision.revision}`} />
          </div>
          {revision.description && <p>Что сделано: {revision.description}</p>}
          {revision.comment && <p>Заметка: {revision.comment}</p>}
          {revision.result === 'link' && revision.content && httpLink(revision.content)
            ? <a href={revision.content} target="_blank" rel="noreferrer">{revision.content}</a>
            : revision.content && <pre>{revision.content}</pre>}
        </li>)}
      </ol>
      {previous && latest && <details className={styles.comparison}>
        <summary>Сравнить версии {previous.revision} и {latest.revision}</summary>
        <div className={styles.compareColumns}>
          <section><h4>Версия {previous.revision}</h4><ResultValue revision={previous} /></section>
          <section><h4>Версия {latest.revision}</h4><ResultValue revision={latest} /></section>
        </div>
      </details>}
    </>}
  </article>;
}

function ResultValue({ revision }: { revision: TaskResultRevision }) {
  if (revision.result === 'link' && revision.content && httpLink(revision.content)) {
    return <a href={revision.content} target="_blank" rel="noreferrer">{revision.content}</a>;
  }
  return <pre>{revision.content || 'Пустой результат'}</pre>;
}

const deliveryPlanFields: Array<[key: string, label: string]> = [
  ['branch', 'Ветка'],
  ['base_branch', 'Базовая ветка'],
  ['remote', 'Remote'],
  ['remote_url', 'Адрес remote'],
  ['commit_message', 'Сообщение коммита'],
  ['pr_title', 'Заголовок PR'],
];

function DeliveryPlanPayload({ payload }: { payload: unknown }) {
  if (payload === undefined || payload === null) {
    return <p role="alert">Содержимое плана недоступно: перед подтверждением проверьте план через CLI.</p>;
  }
  const plan = typeof payload === 'object' && !Array.isArray(payload) ? payload as Record<string, unknown> : {};
  const text = (key: string) => (typeof plan[key] === 'string' ? plan[key] as string : '');
  const files = Array.isArray(plan.files) ? plan.files.filter((file): file is string => typeof file === 'string') : [];
  const fields = deliveryPlanFields.filter(([key]) => text(key));
  return (
    <section className={styles.planContent} aria-label="Содержимое плана доставки">
      {fields.length > 0 && <dl>
        {fields.map(([key, label]) => <div key={key}><dt>{label}</dt><dd>{text(key)}</dd></div>)}
      </dl>}
      {files.length > 0 && <>
        <h4>Файлы ({files.length})</h4>
        <ul>{files.map((file) => <li key={file}><code>{file}</code></li>)}</ul>
      </>}
      {text('pr_body') && <>
        <h4>Описание PR</h4>
        <pre className={styles.planText}>{text('pr_body')}</pre>
      </>}
      <details open={fields.length === 0 && files.length === 0}>
        <summary>План целиком (JSON)</summary>
        <pre className={styles.planText}>{JSON.stringify(payload, null, 2)}</pre>
      </details>
    </section>
  );
}
