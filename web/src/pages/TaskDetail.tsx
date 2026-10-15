import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { FormEvent, KeyboardEvent, ReactNode } from 'react';
import ReactMarkdown from 'react-markdown';
import { Link, useParams } from '../router';
import { classifyIssue, type IssueCode } from './taskIssue';
import {
  answerTaskGrill,
  approveTaskDeliveryPlan,
  cancelRun,
  confirmTaskStage,
  decideApproval,
  finishTaskGrill,
  getActivePrincipal,
  getArtifact,
  getPipelineArtifacts,
  getPipelineRun,
  getTaskGates,
  getTaskRunDetails,
  getTaskRunEvents,
  overrideTaskStageCheck,
  resumeRun,
  resumeRunWithRuntimeUpgrade,
  retryDelivery,
  retryTaskGate,
  returnTaskStage,
  runTaskStageAgent,
  setTaskStageExecutor,
  skipTaskStage,
  submitTaskGrillVersion,
  submitTaskStageResult,
  takeTaskStage,
} from '../api';
import type { Approval, Artifact, Stage, TaskEvent, TaskGates, TaskResultRevision, TaskRunDetails, WorkflowGraph } from '../types';
import { useWebSocket } from '../hooks/useWebSocket';
import { usePageTitle } from '../hooks/usePageTitle';
import { CopyableHash } from '../components/CopyableHash';
import { GateHistory, GatePanel, GateRetryActions } from '../components/GatePanel';
import { Icon } from '../components/Icon';
import { TrackerActions, TrackerDetails, TrackerSummary } from '../components/TrackerPanel';
import { useRunTracker } from '../trackerState';
import { actionVerbs, allowedActionText } from '../actionLabels';
import { mayPerform, permissionRoles, roleLabels } from '../permissions';
import { stageGateFlag } from '../gateView';
import { StageResultForm } from '../components/StageResultForm';
import { httpLink } from '../safeLink';
import type { StageResultSubmission } from '../stageResultValidation';
import { StageRow } from '../components/StageRow';
import { StatusBadge } from '../components/StatusBadge';
import { StageStatusChip } from '../components/StageStatusChip';
import { blockText, executorText, stageStatusEntry, stageSummaryText, visitText } from '../stageStatus';
import { TaskTimeline } from '../components/TaskTimeline';
import { clearStageResultDraft, loadStageResultDraft } from '../stageResultDraft';
import styles from './TaskDetail.module.css';

interface TaskDetailSnapshot {
  detail: TaskRunDetails;
  artifacts: Artifact[];
  events: TaskEvent[];
  gates: TaskGates | null;
}

type RouteState = 'passed' | 'current' | 'upcoming' | 'failed';
type AgentLaunch = { phase: 'idle' | 'starting' | 'started'; at: number; previous: string };
const idleLaunch: AgentLaunch = { phase: 'idle', at: 0, previous: '' };
type ActionKey = 'take' | 'run-agent' | 'results' | 'confirm' | 'answer' | 'new-version' | 'done' | 'plan' | 'skip' | 'override'
  | 'return' | 'executor' | 'decision' | 'resume' | 'grant' | 'cancel' | 'delivery' | 'gate-retry';

const resumableStatuses = ['waiting_for_approval', 'interrupted', 'blocked', 'stopped', 'failed'];
const cancelableStatuses = ['queued', 'running', 'waiting_for_approval'];
const finishedStatuses = ['completed', 'completed_with_warnings'];
const routeLabels: Record<RouteState, string> = { passed: 'пройден', current: 'текущий', upcoming: 'впереди', failed: 'Упал' };

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


function eventTitle(type: string): string {
  const titles: Record<string, string> = {
    run_started: 'Задача создана', run_resumed: 'Задача продолжена', run_paused: 'Задача приостановлена',
    run_resume_failed: 'Не удалось продолжить задачу',
    run_canceled: 'Задача отменена', run_finished: 'Задача завершена', attempt_started: 'Этап начат',
    attempt_finished: 'Этап завершён', attempts_invalidated: 'Результаты этапа пересчитаны',
    approval_requested: 'Запрошено решение', approval_decided: 'Решение принято',
    transition_selected: 'Выбран переход', stage_taken: 'Этап взят участником', stage_agent_requested: 'Участник запустил агента',
    executor_changed: 'Сменён исполнитель этапа', stage_skipped: 'Этап пропущен',
    stage_returned: 'Выполнен возврат', stage_stalled: 'Этап остановился', check_overridden: 'Проверка обойдена администратором', gate_retry_requested: 'Повтор гейта на той же версии',
    gate_attempt_started: 'Гейт запущен', gate_attempt_waiting: 'Гейт ждёт решения', gate_attempt_finished: 'Гейт завершён',
    integration_error: 'Ошибка интеграции', description_missing: 'Нет описания результата',
  };
  return titles[type] ?? type;
}

function currentApproval(details: TaskRunDetails): Approval | undefined {
  const pendingID = details.state?.pending_approval_id;
  return details.approvals.find((approval) => approval.id === pendingID && approval.status === 'pending');
}

function payloadMarkdown(payload: unknown): string | null {
  if (!payload || typeof payload !== 'object') return null;
  const markdown = (payload as { markdown?: unknown }).markdown;
  return typeof markdown === 'string' ? markdown : null;
}

function checkReportPath(payload: unknown): string | null {
  if (!payload || typeof payload !== 'object') return null;
  const path = (payload as { report_path?: unknown }).report_path;
  if (typeof path !== 'string' || !path.endsWith('.md')) return null;
  const name = path.split('/').at(-1);
  return name ? `checks/${name}` : null;
}

function nextStageFromGraph(graph: WorkflowGraph | undefined, stageID: string): string | undefined {
  const edges = graph?.edges.filter((edge) => edge.from === stageID) ?? [];
  return (edges.find((edge) => edge.outcome === 'passed') ?? edges[0])?.to;
}

function clockTime(value: string): string {
  return new Date(value).toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit' });
}

function shortError(value?: string): string {
  if (!value) return '';
  const head = value.split('\n').map((line) => line.trim()).filter(Boolean).slice(0, 2).join(' ');
  return head.length > 240 ? `${head.slice(0, 237)}…` : head;
}

// Старые брифы начинаются с заголовка «# Исходное намерение»: на странице он не нужен.
function stripLeadingHeading(text: string): string {
  const lines = text.split('\n');
  const first = lines.findIndex((line) => line.trim() !== '');
  if (first >= 0 && /^#{1,6}\s/.test(lines[first].trim())) return lines.slice(first + 1).join('\n').replace(/^\s*\n/, '');
  return text;
}

function briefLink(runID: string, path: string): string {
  return `/artifacts/${encodeURIComponent(runID)}/${path.split('/').map(encodeURIComponent).join('/')}`;
}

function isStageResultArtifact(artifact: Artifact, attemptID: string): boolean {
  return artifact.path.startsWith(`attempts/${attemptID}/artifacts/`)
    && !['/status/', '/.stage-check/', '/human/', '/tasks/'].some((part) => artifact.path.includes(part))
    && /\.(md|markdown)$/i.test(artifact.path);
}

function canonicalApprovalRole(role: string): string {
  const value = role.trim().toLowerCase();
  if (['bo', 'po', 'business_owner'].includes(value)) return 'product_owner';
  if (value === 'deployer') return 'release_manager';
  return value;
}

export function TaskDetail() {
  const principal = getActivePrincipal();
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
  const [actor, setActor] = useState(principal?.actor_id ?? 'local-user');
  const [approvalAnswers, setApprovalAnswers] = useState<Record<string, string>>({});
  const [approvalReasons, setApprovalReasons] = useState<Record<string, string>>({});
  const [budgetReason, setBudgetReason] = useState('');
  const [budgetGrantable, setBudgetGrantable] = useState(false);
  const [agentLaunch, setAgentLaunch] = useState<AgentLaunch>(idleLaunch);
  const [resultAccepted, setResultAccepted] = useState(false);
  const [activeKey, setActiveKey] = useState<ActionKey | null>(null);
  const [intentText, setIntentText] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState<TabKey>('result');
  const [viewStageID, setViewStageID] = useState('');
  const resultFormRef = useRef<HTMLDivElement>(null);
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
      const [artifacts, events, gates] = await Promise.all([
        getPipelineArtifacts(details.run.id),
        loadRunEvents(requestedRunID),
        // The gate state is an addition to the page: when it cannot be read the
        // task stays usable and simply shows no gate block.
        getTaskGates(requestedRunID).catch(() => null),
      ]);
      if (!isCurrent()) return;
      const next = { detail: details, artifacts, events, gates };
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
    setApprovalAnswers({});
    setApprovalReasons({});
    setBudgetReason('');
    setAgentLaunch(idleLaunch);
    setResultAccepted(false);
    setActiveTab('result');
    setViewStageID('');
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
  const tracker = useRunTracker(runID, visibleSnapshot);

  // Opening the result form brings it into view: on a narrow screen it sits below the actions.
  useEffect(() => {
    if (showResultForm) resultFormRef.current?.scrollIntoView?.({ block: 'nearest' });
  }, [showResultForm]);

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

  // After "Запустить агента" the page polls until the new attempt shows up, so the status does not wait for the 30 s timer.
  useEffect(() => {
    if (agentLaunch.phase !== 'started') return undefined;
    const startedAt = Date.now();
    const hasFreshAttempt = () => {
      const detail = snapshotRef.current?.detail;
      const stageID = detail?.state?.next_stage;
      return (detail?.stages ?? []).some((stage) => stage.agent_name === stageID && stage.attempt_id !== agentLaunch.previous);
    };
    const timer = window.setInterval(() => {
      if (hasFreshAttempt() || Date.now() - startedAt > 180000) {
        window.clearInterval(timer);
        return;
      }
      void refresh().catch(() => undefined);
    }, 2000);
    return () => window.clearInterval(timer);
  }, [agentLaunch, refresh]);

  const template = details?.template;
  const currentStageID = details?.state?.next_stage ?? '';
  const currentStage = template?.stages.find((stage) => stage.id === currentStageID);
  const currentExecutor = currentStage
    ? details?.state?.executor_overrides?.[currentStage.id]?.executor ?? currentStage.executor
    : 'human';
  // A button needs both: the task state offers the action (allowed_actions) and
  // the server says this viewer may perform it (viewer_actions, the same stage
  // function and system-right checks the handlers enforce).
  const offeredActions = details?.allowed_actions;
  const viewerActions = details?.viewer_actions;
  const allowed = useMemo(() => new Set((offeredActions ?? []).filter((action) =>
    !viewerActions || viewerActions.includes(action))), [offeredActions, viewerActions]);
  const deniedActions = useMemo(() => (viewerActions ? (offeredActions ?? []).filter((action) =>
    !viewerActions.includes(action)) : []), [offeredActions, viewerActions]);
  const pending = details ? currentApproval(details) : undefined;
  // The stage the actions address: the stage whose confirmation waits (the
  // approval's from_stage, as the group heading says), otherwise the stage the
  // computed status names. While a confirmation waits, next_stage is only the
  // destination.
  const projectedStageID = details?.stage_statuses?.current_stage ?? '';
  const actionStageID = pending?.from_stage || projectedStageID || currentStageID;
  const resultsAllowed = allowed.has('results');
  const currentRunID = run?.run_id;
  const runStatus = run?.status;
  const runDbID = run?.id;
  const lastStageAttempt = [...(details?.stages ?? []).filter((stage) => stage.agent_name === currentStageID)]
    .sort((left, right) => left.stage_index - right.stage_index).at(-1);
  const currentOutputArtifacts = (visibleSnapshot?.artifacts ?? []).filter((artifact) =>
    artifact.run_id === currentRunID && !!lastStageAttempt &&
    isStageResultArtifact(artifact, lastStageAttempt.attempt_id));
  const primaryResultPath = currentOutputArtifacts[0]?.path ?? '';

  // A draft saved before a page reload reopens the result form for that stage.
  useEffect(() => {
    if (currentRunID && currentStageID && resultsAllowed && loadStageResultDraft(currentRunID, currentStageID)) {
      setShowResultForm(true);
    }
  }, [currentRunID, currentStageID, resultsAllowed]);
  // The chosen return target belongs to the stage it was picked on.
  useEffect(() => { setReturnTarget(''); }, [actionStageID]);
  useEffect(() => { setAgentLaunch(idleLaunch); setResultAccepted(false); }, [currentStageID]);

  // Бизнес-идея остаётся первоначальной: последующие brief/ содержат ответы и вопросы.
  const briefArtifacts = (visibleSnapshot?.artifacts ?? [])
    .filter((artifact) => artifact.run_id === currentRunID && artifact.path.startsWith('brief/'))
    .sort((left, right) => left.path.localeCompare(right.path));
  const initialBriefPath = briefArtifacts[0]?.path ?? '';
  useEffect(() => {
    setIntentText(null);
    if (!currentRunID || !initialBriefPath) return;
    let active = true;
    getArtifact(currentRunID, initialBriefPath)
      .then((text) => { if (active) setIntentText(text); })
      .catch(() => { if (active) setIntentText(null); });
    return () => { active = false; };
  }, [currentRunID, initialBriefPath]);

  // Частичная выдача бюджета доступна только если сервер так отметил задачу.
  useEffect(() => {
    setBudgetGrantable(false);
    if (runStatus !== 'stopped' || runDbID === undefined) return;
    let active = true;
    getPipelineRun(runDbID)
      .then((data) => { if (active) setBudgetGrantable(data.budget_allowance_grantable === true); })
      .catch(() => { if (active) setBudgetGrantable(false); });
    return () => { active = false; };
  }, [runStatus, runDbID]);

  const returnOptions = template?.returns.filter((route) => route.from === actionStageID) ?? [];
  const effectiveReturnTarget = returnOptions.some((route) => route.to === returnTarget)
    ? returnTarget : returnOptions[0]?.to ?? '';
  const duration = run?.completed_at
    ? Math.max(0, new Date(run.completed_at).getTime() - new Date(run.started_at).getTime())
    : null;

  const perform = async (label: string, action: () => Promise<unknown>, key: ActionKey): Promise<boolean> => {
    const actionRunID = runID;
    const actionStillTargetsCurrentRun = () => activeRunID.current === actionRunID;
    setActionError('');
    setActionStatus(key === 'run-agent' ? '' : `${label}…`);
    setWorking(true);
    setActiveKey(key);
    try {
      await action();
      if (!actionStillTargetsCurrentRun()) return false;
      await refresh();
      if (!actionStillTargetsCurrentRun()) return false;
      setActionStatus('');
      return true;
    } catch (reason) {
      if (!actionStillTargetsCurrentRun()) return false;
      setActionStatus('');
      setActionError(reason instanceof Error ? reason.message : `Не удалось выполнить действие: ${label.toLowerCase()}.`);
      await refresh().catch(() => undefined);
      return false;
    } finally {
      if (actionStillTargetsCurrentRun()) {
        setWorking(false);
        setActiveKey(null);
      }
    }
  };

  const startAgent = async (mode: 'run' | 'refine') => {
    if (!run || !currentStage) return;
    const stageRunID = run.run_id;
    const stageID = currentStage.id;
    const launchedAt = Date.now();
    const previous = lastAgentAttempt?.attempt_id ?? '';
    setAgentLaunch({ phase: 'starting', at: launchedAt, previous });
    const started = await perform('Агент запущен', () => runTaskStageAgent(stageRunID, stageID, mode), 'run-agent');
    setAgentLaunch(started ? { phase: 'started', at: launchedAt, previous } : idleLaunch);
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
    }, 'results');
    if (succeeded) {
      setShowResultForm(false);
      setResultAccepted(true);
    }
  };

  const submitReason = (action: 'skip' | 'override' | 'return') => async (event: FormEvent) => {
    event.preventDefault();
    if (!run || !currentStage) return;
    // The same stage the group heading names: the confirmed one while a
    // transition confirmation waits, never the destination.
    const stageID = actionStageID;
    if (action === 'skip') {
      if (await perform('Этап пропущен', () => skipTaskStage(run.run_id, stageID, skipReason.trim()), 'skip')) setSkipReason('');
    } else if (action === 'override') {
      if (await perform('Проверка обойдена', () => overrideTaskStageCheck(run.run_id, stageID, overrideReason.trim()), 'override')) setOverrideReason('');
    } else {
      if (await perform('Возврат отправлен', () => returnTaskStage(run.run_id, stageID, effectiveReturnTarget, returnReason.trim()), 'return')) setReturnReason('');
    }
  };

  if (loading) return <main className={styles.state}>Загружаем задачу…</main>;
  if (error || !visibleSnapshot || !details || !run) {
    if (!error && !visibleSnapshot) return <main className={styles.state}>Загружаем задачу…</main>;
    return <main className={styles.state} role="alert">{error || 'Задача не найдена.'}</main>;
  }

  const stageTitle = (stageID: string) => (stageID === '$stop'
    ? 'остановка задачи'
    : template?.stages.find((stage) => stage.id === stageID)?.title ?? stageID);
  const canResume = mayPerform(principal, 'run:resume');
  const canCancel = mayPerform(principal, 'run:cancel');
  const resumable = resumableStatuses.includes(run.status) && canResume;
  const cancelable = cancelableStatuses.includes(run.status) && canCancel;
  const finished = finishedStatuses.includes(run.status);
  // Only a system administrator passes a task on after a failed gate (S06); the
  // server decides, the page just does not offer the action to everybody else.
  const canOverride = visibleSnapshot.gates?.can_override === true;
  const canAct = (role: string) => !principal || principal.roles.some((heldRole) => heldRole === canonicalApprovalRole(role));
  const attemptArtifacts = (stage: Stage) => visibleSnapshot.artifacts.filter((artifact) =>
    artifact.path.includes(`attempts/${stage.attempt_id}/`));

  // Что произойдёт после подтверждения текущего этапа.
  const confirmTargetID = pending?.targets?.approve_spec ?? pending?.targets?.approve
    ?? nextStageFromGraph(template?.graph, currentStageID)
    ?? template?.stages[template.stages.findIndex((stage) => stage.id === currentStageID) + 1]?.id;
  const confirmHint = currentStage && allowed.has('confirm')
    ? confirmTargetID ? `После принятия откроется этап «${stageTitle(confirmTargetID)}».`
      : 'После принятия задача завершится.'
    : null;

  // Согласования, которые уже показаны панелями этапа (грилл, план доставки, подтверждение этапа, ввод).
  const grillAllowed = allowed.has('grill/answer') || allowed.has('grill/new-version') || allowed.has('grill/done');
  const handledByStagePanel = (approval: Approval) => approval.status === 'pending' && approval.id === pending?.id && (
    (grillAllowed)
    || (approval.trigger === 'delivery_plan' && allowed.has('plan/approve'))
    || (approval.trigger === 'stage_check' && allowed.has('confirm'))
  );
  const pendingDecisions = details.approvals.filter((approval) => approval.status === 'pending' && !handledByStagePanel(approval));
  // Approvals that ask for an answer are answered in the current stage card, not in the approvals list.
  const questionDecisions = details.approvals.filter((approval) => approval.status === 'pending'
    && (approval.actions.includes('answer_questions') || approval.kind === 'questions') && !(grillAllowed && approval.id === pending?.id));
  // The stage card owns the decision of its own stage even when the run's pending pointer went stale.
  const currentDecision = pendingDecisions.find((approval) => (approval.id === pending?.id || approval.from_stage === currentStageID)
    && !approval.actions.includes('answer_questions') && approval.kind !== 'questions');
  const otherDecisions = pendingDecisions.filter((approval) => approval.id !== currentDecision?.id
    && !questionDecisions.some((question) => question.id === approval.id));
  const decidedDecisions = details.approvals.filter((approval) => approval.status !== 'pending' && payloadMarkdown(approval.payload) === null);
  const runningAttempt = details.stages.find((stage) => stage.status === 'running' && stage.agent_name === currentStageID);
  const latestAttemptFor = (stageID: string) => [...details.stages.filter((stage) => stage.agent_name === stageID)]
    .sort((left, right) => left.stage_index - right.stage_index).at(-1);
  const lastAgentAttempt = lastStageAttempt;
  const latestFailedAttempt = [...details.stages.filter((stage) => stage.status === 'failed')]
    .sort((left, right) => Date.parse(left.started_at) - Date.parse(right.started_at)).at(-1);
  // The failure shown on the current stage: its own failed attempt first, then the latest failure of a failed run.
  const failureAttempt = lastAgentAttempt?.status === 'failed'
    ? lastAgentAttempt : run.status === 'failed' ? latestFailedAttempt : undefined;
  const showFailure = run.status === 'failed' || failureAttempt !== undefined;
  const failureTitle = failureAttempt ? `Этап «${stageTitle(failureAttempt.agent_name)}» упал` : 'Прогон завершился ошибкой';
  const failureReason = shortError(failureAttempt?.error) || shortError(run.error)
    || 'Причина в данных задачи не записана. Откройте лог этапа, чтобы увидеть подробности.';
  const latestResumeOutcome = [...visibleSnapshot.events].reverse().find((event) =>
    event.type === 'run_resume_failed' || event.type === 'run_resumed');
  const resumeFailure = resumableStatuses.includes(run.status) && latestResumeOutcome?.type === 'run_resume_failed'
    ? latestResumeOutcome : undefined;
  const resumeFailureReason = resumeFailure && typeof resumeFailure.data.error === 'string'
    ? resumeFailure.data.error : 'Сервер не записал причину ошибки возобновления.';
  // A task that waits for a person is resumed by that person's decision: «Продолжить задачу» is
  // offered there only after a failed resume or when nobody is asked.
  const resumeOffered = resumable && (run.status !== 'waiting_for_approval' || resumeFailure !== undefined
    || (pendingDecisions.length === 0 && questionDecisions.length === 0));
  // One place for the failure: a resume failure wins over the stage failure it follows.
  // The stage that failed keeps its heading; the newest error (a failed resume, when there is one) is the text.
  const issueSource = showFailure
    ? { heading: failureTitle, text: resumeFailure ? resumeFailureReason : failureReason, attempt: failureAttempt }
    : resumeFailure ? { heading: 'Не удалось продолжить задачу', text: resumeFailureReason, attempt: undefined as Stage | undefined } : null;
  const issue = issueSource ? { ...issueSource, ...classifyIssue(issueSource.text) } : null;


  const viewerMay = (action: string) => !viewerActions || viewerActions.includes(action);
  const route = (template?.stages ?? []).map((stage, index) => {
    const currentIndex = template?.stages.findIndex((item) => item.id === currentStageID) ?? -1;
    let state: RouteState = 'upcoming';
    if (finished || (currentIndex >= 0 && index < currentIndex)) state = 'passed';
    else if (index === currentIndex) state = 'current';
    if (latestAttemptFor(stage.id)?.status === 'failed') state = 'failed';
    // The server projection (S11) is the one source of the status when it is there.
    const server = stageStatusEntry(details.stage_statuses, stage.id);
    if (server) {
      if (server.current) state = 'current';
      else if (server.status === 'passed' || server.status === 'skipped') state = 'passed';
      else if (server.status === 'rejected' || server.status === 'failed' || server.status === 'blocked') state = 'failed';
      else state = 'upcoming';
    }
    return { stage, state, server, current: server ? server.current : index === currentIndex, gateFlag: stageGateFlag(visibleSnapshot.gates, stage.id) };
  });

  // The yellow main button never is a refusal: «Отклонить», a return or a bypass are secondary.
  const isAffirmative = (action: string) => !['reject', 'stop', 'override'].includes(action) && !action.startsWith('return');

  const sendDecision = (value: Approval, role: string, action: string, comment?: string) => perform(
    `Решение: ${approvalLabel(action)}`,
    async () => {
      const resolved = await decideApproval(run.run_id, value, {
        actor_id: principal?.actor_id ?? actor, actor_role: role, action, comment,
        ...(value.artifact_revisions && Object.keys(value.artifact_revisions).length > 0
          ? { artifact_revisions: value.artifact_revisions } : {}),
      });
      if (resolved.status === 'resolved') await resumeRun(run.run_id);
    }, 'decision');

  // Подписи решений берутся из единого словаря действий (actionLabels.ts).
  function approvalLabel(action: string): string {
    if (action === 'answer_questions') return actionVerbs.answerAndContinue;
    if (action === 'run_agent') return actionVerbs.runAgent;
    if (action === 'refine_agent') return actionVerbs.refineWithAgent;
    if (action === 'approve_spec') return `${actionVerbs.accept}: ${stageTitle(nextStageFromGraph(template?.graph, currentStageID) ?? currentStageID)}`;
    if (action === 'approve') return actionVerbs.accept;
    if (action === 'new_version') return actionVerbs.newVersion;
    if (action === 'override') return actionVerbs.overrideCheck;
    if (action === 'return') return actionVerbs.returnStage;
    if (action === 'reject') return actionVerbs.reject;
    if (action === 'stop') return 'Остановить задачу';
    if (action.startsWith('return_to_')) return `${actionVerbs.returnStage}: ${stageTitle(action.slice('return_to_'.length))}`;
    return action;
  }

  const decisionButtonLabel = (approval: Approval, action: string): string => {
    const target = approval.targets?.[action];
    // A return always names where the stage goes back to, so two return buttons are never the same words.
    if (action === 'return' && target) return `${actionVerbs.returnStage}: ${stageTitle(target)}`;
    if ((action === 'approve' || action === 'approve_spec') && target) {
      // A decision that stays at the stage (the human input itself) is plain «Принять»; moving on names where.
      return target === approval.from_stage && approval.trigger === 'human_input' ? actionVerbs.acceptHere : `${actionVerbs.accept}: ${stageTitle(target)}`;
    }
    return approvalLabel(action);
  };

  const requiresReturnReason = (value: Approval, action: string) => value.feedback_actions
    ? value.feedback_actions.includes(action)
    : action.startsWith('return_to_') || (value.kind === 'check_decision' && action === 'return');

  const sendRunCommand = (kind: 'resume' | 'grant' | 'cancel') => {
    if (kind === 'cancel') return perform('Задача отменена', () => cancelRun(run.run_id), 'cancel');
    if (kind === 'grant') {
      return perform('Бюджет выдан, задача продолжена', async () => {
        await resumeRun(run.run_id, { reason: budgetReason.trim() });
        setBudgetReason('');
      }, 'grant');
    }
    return perform('Задача продолжена', () => resumeRun(run.run_id), 'resume');
  };

  const canRetryDelivery = mayPerform(principal, 'run:deliver') &&
    (run.status === 'completed' || run.status === 'completed_with_warnings') &&
    (details.delivery.status === 'pending' || details.delivery.status === 'failed');
  const prURL = safePullRequestURL(details.delivery.record?.pr_url);

  // The stage the server says the task is at; it can be the stage before the
  // one the checkpoint points to while a confirmation of its result is open.
  const serverCurrent = stageStatusEntry(details.stage_statuses, details.stage_statuses?.current_stage ?? '');
  const serverCurrentReturned = serverCurrent?.visits[serverCurrent.active_visit - 1]?.entered_via === 'return';
  const stageState = serverCurrent ? stageSummaryText(serverCurrent, serverCurrentReturned) : (() => {
    if (currentDecision && lastAgentAttempt && ['passed', 'warning'].includes(lastAgentAttempt.status)) {
      return 'Результат готов · ждёт решения';
    }
    if (questionDecisions.length > 0 || currentDecision || otherDecisions.length > 0) return 'Ждёт решения';
    if (runningAttempt) return 'В работе';
    if (lastAgentAttempt?.status === 'blocked' || run.status === 'blocked') return 'Заблокирована';
    if (lastAgentAttempt?.status === 'failed' || run.status === 'failed') return 'Ошибка';
    if (lastAgentAttempt && (lastAgentAttempt.status === 'passed' || lastAgentAttempt.status === 'warning')) return 'Пройдена';
    if (finishedStatuses.includes(run.status)) return 'Завершена';
    return 'Ожидает';
  })();

  // "submit" and "approve" of a human input go through the stage result form, not the approvals list.
  // A stage whose result is only an approval has no content to submit, so «Принять» is the decision itself.
  const stageNeedsContent = (stageID: string) => (template?.stages.find((stage) => stage.id === stageID)?.result ?? 'md') !== 'approve';
  // A return to a configured route is one path: the form of the group «Исключения» (the stage
  // return, with a reason). The same return is not offered a second time as a decision button.
  const returnTargetOf = (approval: Approval, action: string): string | undefined => {
    if (approval.kind === 'check_decision') return undefined;
    if (action.startsWith('return_to_')) return action.slice('return_to_'.length);
    return action === 'return' ? approval.targets?.return : undefined;
  };
  const coveredByReturnForm = (approval: Approval, action: string) => {
    const target = returnTargetOf(approval, action);
    return target !== undefined && allowed.has('return') && approval.from_stage === actionStageID
      && returnOptions.some((route) => route.to === target);
  };
  const decisionActions = (approval: Approval) => (approval.trigger === 'human_input'
    ? approval.actions.filter((action) => action !== 'submit'
      && !(action === 'approve' && stageNeedsContent(approval.from_stage))
      && (!(currentStage?.agent && allowed.has('run-agent')) || !['run_agent', 'refine_agent'].includes(action)))
    : approval.kind === 'check_decision' ? approval.actions.filter((action) => action !== 'override' && action !== 'retry_gate') : approval.actions)
    .filter((action) => !coveredByReturnForm(approval, action));

  const renderDecision = (approval: Approval) => {
    const answer = approvalAnswers[approval.id] ?? '';
    const reason = approvalReasons[approval.id] ?? '';
    const needsAnswer = approval.actions.includes('answer_questions') || approval.kind === 'questions';
    const needsReason = approval.status === 'pending' && decisionActions(approval).some((action) =>
      requiresReturnReason(approval, action));
    return <article key={approval.id} className={styles.approval}>
      <strong>{approval.from_stage === approval.to_stage
        ? `Этап «${stageTitle(approval.from_stage)}»`
        : `«${stageTitle(approval.from_stage)}» → «${stageTitle(approval.to_stage)}»`}</strong>
      <span>Ждёт вашего решения · роли: {approval.required_roles.join(', ') || '—'}
        {approval.quorum === 'all' ? ' · нужны все участники' : ''}</span>
      {needsAnswer && <label className={styles.questionLabel}>Ответ на вопрос
        <textarea value={answer} rows={4} maxLength={16 * 1024}
          onChange={(event) => setApprovalAnswers((current) => ({ ...current, [approval.id]: event.target.value }))} />
      </label>}
      {needsReason && <label className={styles.questionLabel}>Комментарий для доработки или возврата
        <textarea value={reason} maxLength={16 * 1024} rows={3}
          onChange={(event) => setApprovalReasons((current) => ({ ...current, [approval.id]: event.target.value }))} />
      </label>}
      {approval.actions.length > 0 && approval.required_roles.length > 0 && !approval.required_roles.some((role) => canAct(role)) && <p className={styles.questionLabel}>Ответить или решить может участник с ролью: {approval.required_roles.join(', ')}.</p>}
      <div className={styles.actions}>
        {/* One button per action: a person holding two of the required roles does not get the same button twice.
            Only when every role must decide (quorum «all») the button is per role. */}
        {(approval.quorum === 'all' ? approval.required_roles.filter((role) => canAct(role))
          : approval.required_roles.filter((role) => canAct(role)).slice(0, 1))
          .flatMap((role, roleIndex) => decisionActions(approval).map((action) => (
            <button key={`${role}:${action}`} type="button"
              className={approval.trigger !== 'human_input' && roleIndex === 0 && decisionActions(approval).find(isAffirmative) === action && (approval.id === currentDecision?.id || questionDecisions.some((item) => item.id === approval.id)) ? styles.primary : undefined}
              disabled={working ||
              (action === 'answer_questions' && !answer.trim()) ||
              (requiresReturnReason(approval, action) && !reason.trim())}
            onClick={() => void sendDecision(approval, role, action,
              action === 'answer_questions' ? answer : requiresReturnReason(approval, action) ? reason : undefined)}>
              <BusyLabel busy={working && activeKey === 'decision'} text="Сохраняем…">{decisionButtonLabel(approval, action)}
                {approval.quorum === 'all' && approval.required_roles.filter((item) => canAct(item)).length > 1 ? ` (${roleLabels[canonicalApprovalRole(role)] ?? role})` : ''}</BusyLabel>
            </button>
          )))}
      </div>
      <details className={styles.approvalDetails}>
        <summary>Подробности решения</summary>
        <div className={styles.hashes}><span>Хеш решения</span><CopyableHash value={approval.subject_hash} label="хеш решения" /></div>
        {approval.candidate_sha256 && <div className={styles.hashes}><span>Хеш кандидата</span><CopyableHash value={approval.candidate_sha256} label="хеш кандидата" /></div>}
        {approval.artifact_revisions && Object.entries(approval.artifact_revisions).map(([path, revision]) => (
          <code key={path}>{path} · {revision}</code>
        ))}
        {approval.payload != null && payloadMarkdown(approval.payload) === null && <pre><code>{JSON.stringify(approval.payload, null, 2)}</code></pre>}
      </details>
    </article>;
  };

  // The idea block: its first line is the title, the next lines a preview, the whole text sits under «Подробности».
  // Full idea text is mounted only while «Подробности» is open, so the title and preview never repeat in the DOM.
  const ideaBody = (intentText !== null ? stripLeadingHeading(intentText) : (details.task ?? '')).trim();
  const ideaLines = ideaBody.split('\n').map((line) => line.replace(/^#+\s*/, '').trim()).filter(Boolean);
  const ideaTitle = ideaLines[0] ? (ideaLines[0].length > 120 ? `${ideaLines[0].slice(0, 117)}…` : ideaLines[0]) : 'Без названия';

  // Одна строка о том, что сделать прямо сейчас; подробности — в блоках ниже.
  // A pending result decision (approve / decline) is the one action left: start-work cards would only duplicate it.
  const choiceOwnsStage = Boolean(currentDecision && currentDecision.trigger !== 'human_input');
  // A stage whose result is only an approval is accepted by the decision itself: no second «Сдать результат» for it.
  // The decision card already offers «Принять» for the pending approval (decided in the role the approval names):
  // the stage-level «confirm» would be a second button for the same meaning.
  const decisionCoversConfirm = Boolean(currentDecision && pending?.id === currentDecision.id
    && decisionActions(currentDecision).includes('approve'));
  const approveDecidesStage =currentStage?.result === 'approve' && currentDecision?.trigger === 'human_input'
    && decisionActions(currentDecision).includes('approve');
  const nextStep = (() => {
    if (issue) return null;
    // The computed status (S11) leads: while the gates run nobody has a decision to make yet.
    if (serverCurrent?.status === 'checking') return 'Идут гейты этапа. Страница обновится, когда они закончатся.';
    if (currentDecision?.kind === 'check_decision') return 'Проверка не прошла или не завершилась: выберите действие в колонке «Действия».';
    if (currentDecision) return 'Результат ждёт вашего решения: проверьте его и выберите действие в колонке «Действия».';
    if (runningAttempt) return 'Агент работает. Страница обновится, когда он закончит.';
    if (currentStage?.agent && allowed.has('run-agent')) return `Сделайте этап сами или поручите агенту ${currentStage.agent}: потом вы примете результат или измените его.`;
    if (allowed.has('results')) return 'Сдайте результат текстом или ссылкой.';
    if (allowed.has('confirm')) return 'Результат сдан. Примите этап, чтобы перейти дальше.';
    return null;
  })();

  // A run that stopped without a current stage (failed, visit limit) shows the stage it stopped at.
  const stoppedAt = !currentStageID && !finished
    ? [...(details.stage_statuses?.stages ?? [])].filter((entry) => entry.visited).sort((left, right) => left.index - right.index).at(-1) : undefined;
  const viewID = viewStageID && template?.stages.some((stage) => stage.id === viewStageID) ? viewStageID : currentStageID || stoppedAt?.stage_id || '';
  const viewStageDef = template?.stages.find((stage) => stage.id === viewID);
  // The current stage's files are listed in the result card: no empty «Результата пока нет» under it.
  const filesShownAbove = viewID === currentStageID && currentOutputArtifacts.length > 0 && (details.results[viewID] ?? []).length === 0;
  const viewAttempt = viewID ? latestAttemptFor(viewID) : undefined;
  const viewFiles = viewAttempt ? visibleSnapshot.artifacts.filter((artifact) =>
    artifact.run_id === run.run_id && isStageResultArtifact(artifact, viewAttempt.attempt_id)) : [];
  // The gates shown are those of the stage the page is about: while the result of a stage awaits the
  // confirmation of its transition, that is the stage being confirmed, not the one that comes next.
  const gateStageID = serverCurrent?.stage_id || actionStageID || currentStage?.id;
  const currentGateStage = gateStageID ? visibleSnapshot.gates?.stages.find((stage) => stage.stage_id === gateStageID) : undefined;
  const trackerOn = tracker.data?.configured === true;
  const gateAttention = visibleSnapshot.gates?.stages.some((stage) => stageGateFlag(visibleSnapshot.gates, stage.stage_id) !== undefined) ?? false;
  const tabs: { key: TabKey; label: string; flag?: 'danger' | 'warning' }[] = [
    { key: 'result', label: 'Результат' },
    { key: 'gates', label: 'Гейты', flag: gateAttention ? (route.some((item) => item.gateFlag === 'override') ? 'danger' : 'warning') : undefined },
    { key: 'decisions', label: 'Решения' },
    ...(trackerOn ? [{ key: 'tracker' as const, label: 'Трекер' }] : []),
    { key: 'idea', label: 'Описание' },
    { key: 'journal', label: 'Журнал' },
  ];
  const tab: TabKey = tabs.some((item) => item.key === activeTab) ? activeTab : 'result';

  const actorLine = (() => {
    if (finished) return 'Задача завершена: действий от людей не требуется';
    const kind = serverCurrent ? executorText(serverCurrent.executor)
      : currentExecutor === 'agent' ? `Внутренний агент: ${currentStage?.agent ?? '—'}` : 'Человек';
    const owner = details.stage_owners[currentStageID]?.actor_id;
    return owner ? `${kind} · ответственный: ${owner}` : kind;
  })();
  const waitingFor = serverCurrent?.blocked_by && blockText(serverCurrent.blocked_by)
    ? `${blockText(serverCurrent.blocked_by)}${serverCurrent.blocked_by.roles && serverCurrent.blocked_by.roles.length > 0
      ? ` (роли: ${serverCurrent.blocked_by.roles.join(', ')})` : ''}` : '';
  const exceptionsVisible = Boolean(currentStage) && (allowed.has('skip') || (allowed.has('override') && canOverride)
    || (allowed.has('return') && returnOptions.length > 0));
  const needActor = !principal && (questionDecisions.length > 0 || otherDecisions.length > 0);
  // Actions the task offers but this viewer may not perform: no button, a hint naming the role instead.
  const roleForAction = (action: string): string[] => {
    const stage = template?.stages.find((item) => item.id === actionStageID);
    if (action === 'confirm' || action === 'return') return [stage?.approver_function || stage?.function || ''];
    if (action === 'plan/approve') return pending?.required_roles ?? [];
    return [currentStage?.function ?? ''];
  };
  const roleHints = (() => {
    const byRole = new Map<string, string[]>();
    // The administrator right has its own sentence below.
    for (const action of deniedActions.filter((item) => item !== 'override')) {
      const roles = roleForAction(action).filter(Boolean).map((role) => roleLabels[canonicalApprovalRole(role)] ?? role);
      const key = roles.join(', ') || 'другая функция';
      byRole.set(key, [...(byRole.get(key) ?? []), allowedActionText(action)]);
    }
    return [...byRole.entries()].map(([roles, labels]) => ({ roles, labels }));
  })();
  const overrideDenied = (allowed.has('override') && !canOverride) || deniedActions.includes('override');
  // Where the task is: the stage the computed status (S11) names; the checkpoint may already point at the next one.
  const shownStageID = serverCurrent?.stage_id || actionStageID;
  const stageName = stageTitle(shownStageID);
  const hasStageActions = (allowed.size > 0 || Boolean(currentDecision) || questionDecisions.length > 0);
  const gateRetries = currentGateStage?.current?.gates.some((status) => status.retryable) && allowed.has('gate-retry');
  const nothingToDo = !hasStageActions && otherDecisions.length === 0 && !resumeOffered && !cancelable && !canRetryDelivery
    && !(issue?.code === 'version') && !gateRetries;

  return (
    <main className={styles.container}>
      <header className={styles.taskHeader}>
        <h1>{run.feature}</h1>
        <StatusBadge status={run.status} />
        <span className={styles.taskMeta}>{details.mode === 'auto' ? 'Авто' : 'Вручную'}
          {template ? ` · ${template.title || template.template}` : ''} · {new Date(run.started_at).toLocaleDateString('ru-RU')}</span>
        <p className={styles.idea}><span className={styles.ideaLabel}>Бизнес-идея</span> {ideaTitle}
          {' '}<button type="button" className={styles.linkButton} onClick={() => setActiveTab('idea')}>Полное описание</button></p>
        <a className={styles.skipToActions} href="#task-actions">К действиям</a>
      </header>

      <section className={styles.routePanel} aria-labelledby="route-heading">
        <h2 id="route-heading">Маршрут процесса</h2>
        {route.length === 0 ? <p>Этапов нет.</p> : <ol className={styles.route}>
          {route.map(({ stage, state, server, current, gateFlag }, index) => (
            <li key={stage.id} className={styles.routeItem} data-state={state} data-gate-flag={gateFlag}
              aria-current={current ? 'step' : undefined}>
              <button type="button" className={styles.routeButton} aria-pressed={viewID === stage.id}
                title="Показать результат этапа"
                onClick={() => { setViewStageID(stage.id); setActiveTab('result'); }}>
                <span className={styles.routeDot} aria-hidden="true">
                  {state === 'passed' ? <Icon name="passed" size={13} /> : state === 'failed' ? <Icon name="failed" size={13} /> : index + 1}
                </span>
                <span className={styles.routeText}>
                  <span className={styles.routeTitle}>{stage.title}</span>
                  {server ? <>
                    <StageStatusChip status={server.status} />
                    {server.status !== 'not_reached' && <span className={styles.routeState}>{executorText(server.executor)}</span>}
                    {server.visit_count > 1 && <span className={styles.routeState}>
                      Посещений: {server.visit_count}{server.current ? `, сейчас ${server.active_visit}-е` : ''}</span>}
                    {server.status === 'not_reached' && server.visited && <span className={styles.routeState}>Бывал активен ранее</span>}
                    {server.current && server.allowed_actions.some(viewerMay) && <span className={styles.routeState}>
                      Доступно: {server.allowed_actions.filter(viewerMay).map(allowedActionText).join(', ')}</span>}
                  </> : <span className={styles.routeState}>{routeLabels[state]}</span>}
                  {gateFlag === 'override' && <span className={`${styles.gateFlag} ${styles.gateFlagOverride}`}><Icon name="override" size={11} /> Обход провала</span>}
                  {gateFlag === 'advisory' && <span className={`${styles.gateFlag} ${styles.gateFlagAdvisory}`}><Icon name="warning" size={11} /> Рекомендация не выполнена</span>}
                </span>
              </button>
            </li>
          ))}
        </ol>}
      </section>

      <div className={styles.workspace}>
        <aside className={styles.contextCol} aria-label="Статус и контекст" data-testid="context-column">
          <section id="current-stage" className={`${styles.panel} ${styles.statusCard}`} aria-labelledby="current-stage-heading">
            <p className={styles.eyebrow}>{stoppedAt ? 'Остановилась на этапе' : 'Где задача'}</p>
            <h2 id="current-stage-heading">{finished ? 'Задача завершена' : stoppedAt ? stageTitle(stoppedAt.stage_id) : shownStageID ? stageName : 'Этап не выбран'}</h2>
            {serverCurrent ? <StageStatusChip status={serverCurrent.status} /> : null}
            {serverCurrent
              ? serverCurrent.visit_count > 1 && <p className={styles.stageState} data-quiet="true">
                Посещение {serverCurrent.active_visit} из {serverCurrent.visit_count}{serverCurrentReturned ? ' (возврат)' : ''}</p>
              : <p className={styles.stageState} data-state={stageState}>{stageState}</p>}
            <dl className={styles.facts}>
              <div><dt>Кто действует</dt><dd>
                <Icon name={serverCurrent?.executor.kind === 'external_agent' ? 'external'
                  : (serverCurrent?.executor.kind ?? (currentExecutor === 'agent' ? 'internal_agent' : 'human')) === 'human' ? 'human' : 'agent'} size={14} className={styles.factIcon} />
                {actorLine}</dd></div>
              {waitingFor && <div><dt>Чего ждём</dt><dd>{waitingFor}</dd></div>}
              {serverCurrent && serverCurrent.stage_id !== currentStageID && <div><dt>Дальше</dt><dd>
                Сейчас ждёт этап «{stageTitle(serverCurrent.stage_id)}»; после решения откроется «{stageTitle(currentStageID)}».</dd></div>}
              {nextStep && <div><dt>Что делать</dt><dd>{nextStep}</dd></div>}
              {pending && pending.required_roles.length > 0 && <div><dt>Нужны роли</dt><dd>{pending.required_roles.join(', ')}</dd></div>}
            </dl>
            {issue && <IssuePanel runID={run.run_id} issue={issue} />}
            {currentStage && (currentExecutor === 'agent' || agentLaunch.phase !== 'idle') && <AgentStatus runID={run.run_id}
              runningAttempt={runningAttempt} lastAttempt={lastAgentAttempt} launch={agentLaunch} />}
            {currentStage && currentExecutor !== 'agent' && agentLaunch.phase === 'idle' && runningAttempt && <div className={styles.working} role="status">
              <span className={styles.spinner} aria-hidden="true" />
              <p>{`Этап «${currentStage.title}» выполняется, запущено ${clockTime(runningAttempt.started_at)}`}</p>
            </div>}
            {serverCurrent && serverCurrent.visit_count > 1 && <details className={styles.history}>
              <summary>Посещения этапа ({serverCurrent.visit_count})</summary>
              <ol>{serverCurrent.visits.map((visit) => <li key={visit.number}>{visitText(visit, stageTitle)}</li>)}</ol>
            </details>}
            {currentGateStage && <GatePanel stage={currentGateStage} compact />}
          </section>

          <TrackerSummary tracker={tracker} />

          {details.delivery.status !== 'not_requested' && (
            <section className={styles.panel} aria-labelledby="delivery-heading">
              <h2 id="delivery-heading">Git-доставка</h2>
              <p aria-live="polite">Статус: {deliveryStatusText(details.delivery.status)}</p>
              {details.delivery.record && (
                <div>
                  {details.delivery.record.commit_sha && <p>Коммит: <code>{details.delivery.record.commit_sha}</code></p>}
                  {details.delivery.record.pr_url && <p>Pull request: {prURL
                    ? <a href={prURL} target="_blank" rel="noreferrer">{details.delivery.record.pr_url}</a>
                    : <code>{details.delivery.record.pr_url}</code>}</p>}
                </div>
              )}
              {details.delivery.error && <p className={styles.deliveryError} role="status">Ошибка доставки: {details.delivery.error}</p>}
            </section>
          )}
        </aside>

        <>
          {(questionDecisions.length > 0 || (grillAllowed && pending) || (currentDecision?.kind === 'check_decision')
            || (allowed.has('plan/approve') && pending?.trigger === 'delivery_plan') || (showResultForm && allowed.has('results'))) && (
            <section className={`${styles.panel} ${styles.attention}`} aria-label="Требует внимания">
              {questionDecisions.length > 0 && <section className={styles.questions} aria-labelledby="question-heading">
                <h3 id="question-heading">Для этапа «{currentStage?.title ?? currentStageID}» нужно уточнение владельца продукта</h3>
                {questionDecisions.some((approval) => payloadMarkdown(approval.payload) === null)
                  && <p>Текст вопросов не передан в запросе: откройте артефакт этапа.</p>}
                {questionDecisions.map((approval) => <div key={approval.id}>
                  {payloadMarkdown(approval.payload) !== null && <div className={styles.markdown}>
                    <ReactMarkdown>{payloadMarkdown(approval.payload) || 'Текст вопроса находится в артефакте этапа.'}</ReactMarkdown></div>}
                  <p className={styles.sectionNote}>Ответ вводится в колонке «Действия».</p>
                </div>)}
              </section>}
              {grillAllowed && pending && <GrillQuestions approval={pending} />}
              {currentDecision?.kind === 'check_decision' && <section className={styles.checkDecision} aria-label="Проверка не пройдена">
                <h3>Проверка не прошла или не завершилась</h3>
                <p>Откройте отчёт и отправьте результат на повторную проверку.</p>
                {checkReportPath(currentDecision.payload) && <Link to={briefLink(run.run_id, checkReportPath(currentDecision.payload)!)}>Открыть отчёт проверки</Link>}
              </section>}
              {allowed.has('plan/approve') && pending?.trigger === 'delivery_plan' && <section aria-label="План доставки">
                <h3>План доставки ожидает принятия</h3>
                <DeliveryPlanPayload payload={pending.payload} />
              </section>}
              {currentStage && showResultForm && allowed.has('results') && (
                <div ref={resultFormRef}>
                  <StageResultForm key={`${run.run_id}:${currentStage.id}`} runId={run.run_id} stage={currentStage}
                    working={working} onSubmit={(value) => void submitResult(value)} onCancel={() => setShowResultForm(false)} />
                </div>
              )}
            </section>
          )}

          <section className={`${styles.panel} ${styles.tabsPanel}`}>
            <TabBar tabs={tabs} active={tab} onChange={setActiveTab} />
            <div role="tabpanel" id={`tab-panel-${tab}`} aria-labelledby={`tab-${tab}`} className={styles.tabBody} tabIndex={0}>
              {tab === 'result' && <div className={styles.resultTab}>
                {viewID === currentStageID && (currentOutputArtifacts.length > 0 || (details.results[currentStageID] ?? []).length > 0) &&
                  <section className={styles.currentResult} aria-labelledby="current-result-heading">
                    <div className={styles.resultHeading}>
                      <h3 id="current-result-heading">Результат этапа</h3>
                      {lastAgentAttempt && <span>Агент завершил работу · попытка {lastAgentAttempt.stage_index}</span>}
                    </div>
                    <p>{currentDecision ? 'Результат ждёт решения: откройте файл, затем примите его или отправьте на доработку.' : 'Результат сохранён.'}</p>
                    {primaryResultPath ? <div className={styles.resultFiles}>
                      {currentOutputArtifacts.map((artifact) => <Link key={artifact.path} to={briefLink(run.run_id, artifact.path)}>
                        Открыть {artifact.name}
                      </Link>)}
                    </div> : (details.results[currentStageID] ?? []).at(-1)?.result === 'link' &&
                      (httpLink((details.results[currentStageID] ?? []).at(-1)?.content ?? '')
                        ? <a href={(details.results[currentStageID] ?? []).at(-1)?.content} target="_blank" rel="noreferrer">Открыть результат</a>
                        : <code>{(details.results[currentStageID] ?? []).at(-1)?.content}</code>)}
                  </section>}
                {!filesShownAbove && <h3 className={styles.viewHeading}>Этап «{viewStageDef?.title ?? viewID}»{currentStageID && viewID !== currentStageID ? ' (не текущий)' : ''}</h3>}
                {!filesShownAbove && <ResultHistory title={viewStageDef?.title ?? viewID} stageID={viewID} revisions={details.results[viewID] ?? []}
                  artifacts={viewID === currentStageID && currentOutputArtifacts.length > 0 ? [] : viewFiles} runID={run.run_id} />}
                {briefArtifacts.length > 1 && <details className={styles.briefHistory}>
                  <summary>Ответы и уточнения ({briefArtifacts.length - 1})</summary>
                  <div className={styles.resultFiles}>{briefArtifacts.slice(1).map((artifact, index) =>
                    <Link key={artifact.path} to={briefLink(run.run_id, artifact.path)}>Уточнение {index + 1}</Link>)}</div>
                </details>}
                {(() => {
                  const empty = (template?.stages ?? []).filter((stage) => (details.results[stage.id] ?? []).length === 0
                    && !latestAttemptFor(stage.id));
                  return empty.length > 0 ? <p className={styles.sectionNote}>Результата ещё нет у этапов: {empty.map((stage) => stage.title).join(', ')}.</p> : null;
                })()}
              </div>}

              {tab === 'gates' && <div className={styles.gatesTab}>
                {currentGateStage?.current
                  ? <GatePanel stage={currentGateStage} />
                  : <p>У текущего этапа нет гейтов или они ещё не запускались.</p>}
                <h3>Обходы и рекомендации по всей задаче</h3>
                <GateHistory gates={visibleSnapshot.gates} />
              </div>}

              {tab === 'decisions' && <div className={styles.decisionsTab}>
                <h3>История согласований</h3>
                {decidedDecisions.length === 0 ? <p>Решений пока не принято.</p> : decidedDecisions.map((approval) => <article key={approval.id} className={styles.approval}>
                  <strong>{stageTitle(approval.from_stage)}{approval.to_stage !== approval.from_stage ? ` → ${stageTitle(approval.to_stage)}` : ''}</strong>
                  <span>решено: {approval.resolved_action || 'решение принято'}
                    {approval.resolved_at && <> · <time dateTime={approval.resolved_at}>{new Date(approval.resolved_at).toLocaleString('ru-RU')}</time></>}</span>
                  {approval.decisions?.map((decision, index) => <small key={`${decision.actor_id}-${index}`}>
                    {decision.actor_id} ({decision.actor_role}): {decision.action}{decision.comment ? ` — ${decision.comment}` : ''}
                  </small>)}
                </article>)}
                <h3 id="grill-history-heading">История грилла</h3>
                <GrillHistory approvals={details.approvals.filter((approval) =>
                  payloadMarkdown(approval.payload) !== null && approval.status !== 'pending')} />
              </div>}

              {tab === 'tracker' && <TrackerDetails runId={run.run_id} tracker={tracker} />}

              {tab === 'idea' && <div className={styles.ideaTab}>
                <h3>Бизнес-идея</h3>
                {ideaBody ? <div className={styles.markdown}><ReactMarkdown>{ideaBody}</ReactMarkdown></div> : <p>Текст идеи пока не записан.</p>}
                {(details.external_links ?? []).length > 0 && (
                  <ul className={styles.externalLinks} aria-label="Внешние ссылки">
                    {details.external_links?.map((value) => (
                      <li key={value}>{httpLink(value)
                        ? <a href={value} target="_blank" rel="noreferrer">{value}</a>
                        : <code>{value}</code>}</li>
                    ))}
                  </ul>
                )}
                <dl className={styles.metaList}>
                  <div><dt>ID задачи</dt><dd><CopyableHash value={run.run_id} label="ID задачи" /></dd></div>
                  <div><dt>Фича</dt><dd><code>{run.feature}</code></dd></div>
                  <div><dt>Режим</dt><dd>{details.mode === 'auto' ? 'Авто' : 'Вручную'}</dd></div>
                  <div><dt>Создана</dt><dd>{new Date(run.started_at).toLocaleString('ru-RU')}</dd></div>
                  <div><dt>Длительность</dt><dd>{duration !== null ? `${(duration / 1000).toFixed(1)} с` : '—'}</dd></div>
                  {template && <div><dt>Шаблон</dt><dd>{template.title || template.template} · <code title={template.version}>{template.version.slice(0, 12)}…</code></dd></div>}
                </dl>
              </div>}

              {tab === 'journal' && <div className={styles.journalTab}>
                <section aria-labelledby="timeline-heading">
                  <h3 id="timeline-heading">Таймлайн задачи</h3>
                  <TaskTimeline details={details} events={visibleSnapshot.events} eventTitle={eventTitle} />
                </section>
                <section aria-labelledby="attempts-heading">
                  <h3 id="attempts-heading">Этапы, проверки и логи</h3>
                  {details.stages.length === 0 ? <p>Попыток пока нет.</p> : <div className={styles.attempts}>
                    {details.stages.map((stage) => {
                      const title = template?.stages.find((item) => item.id === stage.agent_name)?.title ?? stage.agent_name;
                      return <article key={stage.id} className={styles.attempt}>
                        <h4>{title} · попытка {stage.stage_index}</h4>
                        <div className={styles.attemptMeta}><code>{stage.attempt_id}</code><StatusBadge status={stage.status} />
                          <a href={`/api/runs/${encodeURIComponent(run.run_id)}/logs/${encodeURIComponent(stage.attempt_id)}`}
                            target="_blank" rel="noreferrer">{actionVerbs.openLog}</a>
                        </div>
                        <StageRow stage={stage} artifacts={attemptArtifacts(stage)} runId={run.run_id} />
                      </article>;
                    })}
                  </div>}
                </section>
                <section aria-labelledby="events-heading">
                  <h3 id="events-heading">Лента событий задачи</h3>
                  {visibleSnapshot.events.length === 0 ? <p>Событий пока нет.</p> : <ol className={styles.events}>
                    {visibleSnapshot.events.map((event) => <li key={event.cursor} id={`task-event-${event.cursor}`} data-severity={event.severity}>
                      <time dateTime={event.timestamp}>{new Date(event.timestamp).toLocaleString('ru-RU')}</time>
                      <strong>{eventTitle(event.type)}</strong>
                      <span>{[event.stage, event.actor, event.executor, event.attempt_id].filter(Boolean).join(' · ')}</span>
                      {event.type === 'check_overridden' && <span className={styles.overrideEvent}>Обход провала: исход гейта остаётся «провал»</span>}
                      {event.type !== 'check_overridden' && event.severity === 'warning' && <span className={styles.warning}>Предупреждение</span>}
                      {Object.keys(event.data).length > 0 && <details>
                        <summary>Подробности</summary><pre>{JSON.stringify(event.data, null, 2)}</pre>
                      </details>}
                    </li>)}
                  </ol>}
                </section>
              </div>}
            </div>
          </section>
        </>
        <aside className={styles.actionCol} id="task-actions" tabIndex={-1} aria-label="Действия" data-testid="action-column">
          <h2>Действия</h2>
          {actionStatus && <p className={styles.actionStatus} role="status">{actionStatus}</p>}
          {working && <p className={styles.waiting} role="status"><span className={styles.buttonSpinner} aria-hidden="true" />Ждём ответ сервера…</p>}
          {actionError && <p className={styles.actionError} role="alert">{actionError}</p>}
          {resultAccepted && <p className={styles.resultAccepted} role="status">Результат принят</p>}
          {needActor && <label className={styles.questionLabel}>Ваш идентификатор
            <input value={actor} onChange={(event) => setActor(event.target.value)} aria-label="Идентификатор участника" />
          </label>}

          {nothingToDo && <p className={styles.noActions} data-testid="no-actions">
            {finished ? 'Задача завершена: действий нет.' : `Сейчас у вас нет доступных действий. ${waitingFor ? `Ждём: ${waitingFor}.` : stageState + '.'}`}
            {principal && principal.roles.length > 0 ? ` Ваши роли: ${principal.roles.map((role) => roleLabels[role] ?? role).join(', ')}.` : ''}
            {resumableStatuses.includes(run.status) && !canResume ? ` Продолжить задачу могут: ${permissionRoles['run:resume']}.` : ''}
            {cancelableStatuses.includes(run.status) && !canCancel ? ` Отменить задачу могут: ${permissionRoles['run:cancel']}.` : ''}
          </p>}

          {roleHints.length > 0 && <p className={styles.noActions} data-testid="role-needed">
            {roleHints.map((hint) => `Нужна роль: ${hint.roles}. Без неё недоступно: ${hint.labels.join(', ')}.`).join(' ')}
          </p>}

          {hasStageActions && <section className={styles.group} aria-label={`Этап «${stageName}»`}>
            <h3>Этап «{stageName}»</h3>
            {allowed.has('confirm') && !decisionCoversConfirm && <button type="button" className={styles.primary} disabled={working || !pending?.subject_hash}
              title={pending?.subject_hash ? undefined : 'Запрос решения ещё не загружен: подождите обновления страницы'}
              onClick={() => void perform('Этап принят', () => confirmTaskStage(run.run_id, actionStageID, pending?.subject_hash ?? ''), 'confirm')}>
              <BusyLabel busy={working && activeKey === 'confirm'} text="Сохраняем…">{confirmTargetID ? `${actionVerbs.accept}: ${stageTitle(confirmTargetID)}` : actionVerbs.acceptFinal}</BusyLabel></button>}
            {confirmHint && <p className={styles.sectionNote}>{confirmHint}</p>}
            {questionDecisions.map(renderDecision)}
            {currentDecision && decisionActions(currentDecision).length > 0 && renderDecision(currentDecision)}

            {currentStage && (allowed.has('take') || allowed.has('results') || allowed.has('run-agent')) && !choiceOwnsStage && <>
              {allowed.has('results') && !showResultForm && !approveDecidesStage && <button type="button" className={currentExecutor !== 'agent' ? styles.primary : undefined} disabled={working}
                onClick={() => setShowResultForm(true)}><Icon name="human" size={14} /> {actionVerbs.submitResult}</button>}
              {allowed.has('results') && showResultForm && <p className={styles.sectionNote}>Форма результата открыта в основной области.</p>}
              {allowed.has('take') && !details.stage_owners[currentStageID] && <button type="button" disabled={working}
                onClick={() => void perform('Этап взят', () => takeTaskStage(run.run_id, currentStageID), 'take')}>
                <BusyLabel busy={working && activeKey === 'take'} text="Сохраняем…">{actionVerbs.takeStage}</BusyLabel>
              </button>}
              {allowed.has('run-agent') && currentStage?.agent && <button type="button" className={currentExecutor === 'agent' ? styles.primary : undefined} disabled={working}
                onClick={() => void startAgent(pending?.actions.includes('retry') ? 'refine' : 'run')}>
                <BusyLabel busy={working && activeKey === 'run-agent'} text="Запускаем…"><Icon name="agent" size={14} /> {pending?.actions.includes('retry') ? actionVerbs.refineWithAgent : actionVerbs.runAgent}</BusyLabel>
              </button>}
              {allowed.has('run-agent') && currentStage?.agent && <p className={styles.sectionNote}>Агент: {currentStage?.agent}. Результат вы примете сами.</p>}
            </>}

            {allowed.has('executor') && currentStage?.agent && currentExecutor === 'agent' && <div className={styles.inlineRow}>
              <span>Исполнитель этапа</span>
              <button type="button" disabled={working}
                onClick={() => void perform('Исполнитель сменён', () => setTaskStageExecutor(run.run_id, currentStageID, 'human'), 'executor')}>
                {actionVerbs.changeExecutor}: на человека
              </button>
            </div>}

            {grillAllowed && pending && <GrillAnswerForm approval={pending} answer={grillAnswer} setAnswer={setGrillAnswer} disabled={working} activeKey={activeKey}
              canAnswer={allowed.has('grill/answer')} canNewVersion={allowed.has('grill/new-version')} canDone={allowed.has('grill/done')}
              onAnswer={() => void perform('Ответ отправлен', () => answerTaskGrill(run.run_id, currentStageID, grillAnswer.trim()), 'answer')}
              onNewVersion={() => void perform('Новая версия отправлена', () =>
                submitTaskGrillVersion(run.run_id, currentStageID, grillAnswer.trim()), 'new-version')}
              onDone={() => void perform('Грилл завершён', () => finishTaskGrill(run.run_id, currentStageID), 'done')} />}

            {allowed.has('plan/approve') && pending?.trigger === 'delivery_plan' && <div className={styles.planApproval}>
              <p>Хеш плана: <CopyableHash value={pending.subject_hash} label="хеш плана" /></p>
              <button type="button" className={styles.primary} disabled={working} onClick={() => void perform('План принят', () =>
                approveTaskDeliveryPlan(run.run_id, pending.subject_hash), 'plan')}><BusyLabel busy={working && activeKey === 'plan'} text="Сохраняем…">{actionVerbs.acceptPlan}</BusyLabel></button>
            </div>}

            {currentStage && gateRetries && <GateRetryActions stage={currentGateStage} working={working}
              onRetry={(gate) => void perform('Гейт запущен повторно', () => retryTaskGate(run.run_id, currentStageID, gate), 'gate-retry')} />}
          </section>}

          {issue?.code === 'version' && <section className={styles.group} aria-label="Версия задачи">
            <h3>Версия задачи</h3>
            <button type="button" disabled={working}
              onClick={() => void perform('Задача продолжена на текущей версии', () => resumeRunWithRuntimeUpgrade(run.run_id), 'resume')}>
              <BusyLabel busy={working && activeKey === 'resume'} text="Запускаем…">{actionVerbs.resumeOnCurrentVersion}</BusyLabel>
            </button>
          </section>}

          {currentStage && exceptionsVisible && <section className={`${styles.group} ${styles.exceptions}`} aria-label="Исключения">
            <h3>Исключения</h3>
            <p className={styles.sectionNote}>Нужна причина: она попадёт в журнал.</p>
            {allowed.has('skip') && <form className={styles.reasonForm} onSubmit={submitReason('skip')}>
              <label>Причина пропуска
                <input value={skipReason} onChange={(event) => setSkipReason(event.target.value)} required maxLength={4000} />
              </label>
              <button disabled={working || !skipReason.trim()}>{actionVerbs.skip}</button>
            </form>}
            {allowed.has('return') && returnOptions.length > 0 && <form className={styles.reasonForm} onSubmit={submitReason('return')}>
              <label>Вернуть на этап
                <select value={effectiveReturnTarget} onChange={(event) => setReturnTarget(event.target.value)}>
                  {returnOptions.map((route) => <option key={route.to} value={route.to}>{stageTitle(route.to)}</option>)}
                </select>
              </label>
              <label>Причина возврата
                <input value={returnReason} onChange={(event) => setReturnReason(event.target.value)} required maxLength={4000} />
              </label>
              <button disabled={working || !returnReason.trim()}>{actionVerbs.returnStage}: {stageTitle(effectiveReturnTarget)}</button>
            </form>}
            {allowed.has('override') && canOverride && <form className={`${styles.reasonForm} ${styles.overrideForm}`} onSubmit={submitReason('override')}>
              <p>Только для администратора. Исход гейта останется «провал»; обход запишется с вашим именем, причиной и версией результата.</p>
              <label>Причина обхода проверки
                <input value={overrideReason} onChange={(event) => setOverrideReason(event.target.value)} required maxLength={4000} />
              </label>
              <button disabled={working || !overrideReason.trim()}>{actionVerbs.overrideCheck}</button>
            </form>}
          </section>}
          {currentStage && overrideDenied && <p className={styles.noActions}>
            Провести задачу дальше после провала обязательного гейта может только администратор.</p>}

          {otherDecisions.length > 0 && <section className={styles.group} aria-label="Другие запросы решения">
            <h3>Другие запросы решения</h3>
            {otherDecisions.map(renderDecision)}
          </section>}

          {(resumeOffered || cancelable || canRetryDelivery) && <section className={styles.group} aria-label="Управление задачей">
            <h3>Задача целиком</h3>
            {resumeOffered && <>
              <button type="button" className={hasStageActions ? undefined : styles.primary} disabled={working} onClick={() => void sendRunCommand('resume')}><BusyLabel busy={working && activeKey === 'resume'} text="Запускаем…">{actionVerbs.resumeTask}</BusyLabel></button>
              <p className={styles.sectionNote}>Запускает задачу снова с закреплённой версией шаблона и с текущего места.</p>
              {run.status === 'stopped' && budgetGrantable && (
                <div className={styles.budgetGrant}>
                  <p>Задачу остановил бюджет. «{actionVerbs.resumeTask}» новый бюджет не выдаёт. Выдать ещё один бюджет того же размера можно явным решением с причиной; дневной бюджет не выдаётся.</p>
                  <input value={budgetReason} onChange={(event) => setBudgetReason(event.target.value)}
                    aria-label="Причина выдачи бюджета" placeholder="Причина выдачи бюджета" />
                  <button type="button" disabled={working || budgetReason.trim() === ''} onClick={() => void sendRunCommand('grant')}>
                    <BusyLabel busy={working && activeKey === 'grant'} text="Запускаем…">{actionVerbs.grantBudget}</BusyLabel>
                  </button>
                </div>
              )}
            </>}
            {canRetryDelivery && <button type="button" disabled={working}
              onClick={() => void perform('Доставка повторена', () => retryDelivery(run.run_id), 'delivery')}>
              <BusyLabel busy={working && activeKey === 'delivery'} text="Запускаем…">{actionVerbs.retryDelivery}</BusyLabel>
            </button>}
            {cancelable && <button type="button" disabled={working} onClick={() => void sendRunCommand('cancel')}>
              <BusyLabel busy={working && activeKey === 'cancel'} text="Сохраняем…">{actionVerbs.cancelTask}</BusyLabel></button>}
          </section>}

          <TrackerActions runId={run.run_id} tracker={tracker} />
        </aside>

      </div>
    </main>
  );
}

function deliveryStatusText(status: string): string {
  const labels: Record<string, string> = {
    pending: 'ждёт выполнения', failed: 'ошибка', recorded: 'выполнена', unavailable: 'недоступна',
  };
  return labels[status] ?? status;
}

function safePullRequestURL(value?: string): string | null {
  if (!value) return null;
  try {
    const url = new URL(value);
    return url.protocol === 'https:' && !url.username && !url.password ? url.toString() : null;
  } catch {
    return null;
  }
}

type TabKey = 'result' | 'gates' | 'decisions' | 'tracker' | 'idea' | 'journal';

// Вкладки основной области: стрелки, Home и End переключают их с клавиатуры.
function TabBar({ tabs, active, onChange }: {
  tabs: { key: TabKey; label: string; flag?: 'danger' | 'warning' }[];
  active: TabKey;
  onChange: (key: TabKey) => void;
}) {
  const refs = useRef<Record<string, HTMLButtonElement | null>>({});
  const move = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    let next = index;
    if (event.key === 'ArrowRight') next = (index + 1) % tabs.length;
    else if (event.key === 'ArrowLeft') next = (index - 1 + tabs.length) % tabs.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = tabs.length - 1;
    else return;
    event.preventDefault();
    onChange(tabs[next].key);
    refs.current[tabs[next].key]?.focus();
  };
  return <div role="tablist" aria-label="Материалы задачи" className={styles.tabs}>
    {tabs.map((item, index) => <button key={item.key} type="button" role="tab" id={`tab-${item.key}`}
      ref={(node) => { refs.current[item.key] = node; }}
      aria-selected={active === item.key} aria-controls={active === item.key ? `tab-panel-${item.key}` : undefined} tabIndex={active === item.key ? 0 : -1}
      className={styles.tab} data-flag={item.flag}
      onClick={() => onChange(item.key)} onKeyDown={(event) => move(event, index)}>
      {item.label}
      {item.flag && <span className={styles.tabFlag} data-flag={item.flag}
        aria-label={item.flag === 'danger' ? 'есть обход провала' : 'есть невыполненные рекомендации'} />}
    </button>)}
  </div>;
}

function BusyLabel({ busy, text, children }: { busy: boolean; text: string; children: ReactNode }) {
  if (!busy) return <>{children}</>;
  return <><span className={styles.buttonSpinner} aria-hidden="true" />{text}</>;
}

function IssuePanel({ runID, issue }: {
  runID: string;
  issue: { heading: string; text: string; code: IssueCode; explanation: string; attempt?: Stage };
}) {
  const attempt = issue.attempt;
  return <div className={styles.failure} role="status" data-testid="stage-failure">
    <div className={styles.failureHead}>
      <Icon name="failed" size={18} />
      <p className={styles.failureTitle}>{issue.heading}</p>
    </div>
    <p className={styles.failureReason}>{issue.text}</p>
    <p>{issue.explanation}</p>
    <details className={styles.details}>
      <summary>Подробности</summary>
      <p>{issue.text}</p>
      {attempt && <a href={`/api/runs/${encodeURIComponent(runID)}/logs/${encodeURIComponent(attempt.attempt_id)}`}
        target="_blank" rel="noreferrer">{actionVerbs.openLog}</a>}
    </details>
  </div>;
}

function AgentStatus({ runID, runningAttempt, lastAttempt, launch }: {
  runID: string;
  runningAttempt?: Stage;
  lastAttempt?: Stage;
  launch: AgentLaunch;
}) {
  // After a launch, an attempt that started earlier belongs to the previous run and is not shown as the outcome.
  const current = launch.phase === 'started' && lastAttempt?.attempt_id === launch.previous ? undefined : lastAttempt;
  const attempt = runningAttempt ?? current;
  let state: 'running' | 'starting' | 'started' | 'finished' | 'idle' = 'idle';
  let title = '';
  if (runningAttempt) {
    state = 'running';
    title = 'Агент работает…';
  } else if (launch.phase === 'starting') {
    state = 'starting';
    title = 'Запускаем агента…';
  } else if (launch.phase === 'started' && !current) {
    state = 'started';
    title = 'Агент запущен';
  } else if (current && (current.status === 'passed' || current.status === 'warning')) {
    state = 'finished';
    title = 'Агент завершил';
  }
  if (state === 'idle') return null;
  return <div className={styles.agentStatus} role="status" data-state={state}>
    {state === 'running'
      ? <span className={styles.spinner} aria-hidden="true" />
      : <Icon name={state === 'finished' ? 'passed' : 'agent'} size={18} />}
    <div>
      <p className={styles.agentTitle}>{title}</p>
      {attempt && <a href={`/api/runs/${encodeURIComponent(runID)}/logs/${encodeURIComponent(attempt.attempt_id)}`}
        target="_blank" rel="noreferrer">{actionVerbs.openLog}</a>}
    </div>
  </div>;
}

// Вопросы грилла — материал для основной области; ответ вводится в колонке действий.
function GrillQuestions({ approval }: { approval: Approval }) {
  const payload = approval.payload && typeof approval.payload === 'object'
    ? approval.payload as { markdown?: unknown } : null;
  const markdown = typeof payload?.markdown === 'string' ? payload.markdown : '';
  return <section className={styles.grill} aria-labelledby="grill-heading">
    <h3 id="grill-heading">Грилл: вопросы и ответы</h3>
    {markdown ? <div className={styles.markdown}><ReactMarkdown>{markdown}</ReactMarkdown></div>
      : <p>Вопросы доступны в данных запроса.</p>}
    {approval.decisions?.map((decision, index) => <article key={`${decision.actor_id}-${index}`} className={styles.answer}>
      <strong>{decision.actor_id} · {decision.action}</strong>
      {decision.comment && <p>{decision.comment}</p>}
    </article>)}
  </section>;
}

function GrillAnswerForm({ answer, setAnswer, disabled, canAnswer, canNewVersion, canDone, onAnswer, onNewVersion, onDone, activeKey }: {
  approval: Approval;
  activeKey: ActionKey | null;
  answer: string;
  setAnswer: (value: string) => void;
  disabled: boolean;
  canAnswer: boolean;
  canNewVersion: boolean;
  canDone: boolean;
  onAnswer: () => void;
  onNewVersion: () => void;
  onDone: () => void;
}) {
  return <div className={styles.grillForm}>
    {(canAnswer || canNewVersion) && <label>{canAnswer ? 'Ваш ответ' : 'Комментарий к новой версии'}
      <textarea value={answer} onChange={(event) => setAnswer(event.target.value)} rows={4} maxLength={16 * 1024} />
    </label>}
    {canNewVersion && <p className={styles.sectionNote}>Чтобы исправить результат, откройте артефакт этапа и нажмите «Предложить изменение»,
      затем отправьте новую версию. Текст выше уйдёт как комментарий к ней.</p>}
    {canAnswer && <button type="button" className={styles.primary} disabled={disabled || !answer.trim()} onClick={onAnswer}><BusyLabel busy={disabled && activeKey === 'answer'} text="Отправляем…">{actionVerbs.answerAndContinue}</BusyLabel></button>}
    {canNewVersion && <button type="button" disabled={disabled} onClick={onNewVersion}><BusyLabel busy={disabled && activeKey === 'new-version'} text="Отправляем…">{actionVerbs.newVersion}</BusyLabel></button>}
    {canDone && <button type="button" disabled={disabled} onClick={onDone}><BusyLabel busy={disabled && activeKey === 'done'} text="Сохраняем…">{actionVerbs.finishGrill}</BusyLabel></button>}
  </div>;
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

function ResultHistory({ title, stageID, revisions, artifacts, runID }: {
  title: string;
  stageID: string;
  revisions: TaskResultRevision[];
  artifacts: Artifact[];
  runID: string;
}) {
  const previous = revisions.length > 1 ? revisions[revisions.length - 2] : null;
  const latest = revisions.at(-1);
  return <article id={`result-${stageID}`} className={styles.resultStage} aria-label={`Результат этапа ${title}`}>
    {artifacts.length > 0 && <div className={styles.resultFiles}>
      {artifacts.map((artifact) => <Link key={artifact.path} to={briefLink(runID, artifact.path)}>
        {artifact.name}
      </Link>)}
    </div>}
    {revisions.length === 0 ? artifacts.length === 0 && <p>Результата пока нет.</p> : <>
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
    return <p role="alert">Содержимое плана недоступно: перед принятием проверьте план через CLI.</p>;
  }
  const plan = typeof payload === 'object' && !Array.isArray(payload) ? payload as Record<string, unknown> : {};
  const text = (key: string) => (typeof plan[key] === 'string' ? plan[key] as string : '');
  const files = Array.isArray(plan.files) ? plan.files.filter((file): file is string => typeof file === 'string') : [];
  const fields = deliveryPlanFields.filter(([key]) => text(key));
  const empty = fields.length === 0 && files.length === 0 && !text('pr_body');
  return (
    <section className={styles.planContent} aria-label="Содержимое плана доставки">
      {empty && <p role="alert">В плане нет ни ветки, ни файлов, ни описания PR: перед принятием проверьте план через CLI.</p>}
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
