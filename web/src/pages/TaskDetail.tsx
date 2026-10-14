import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { CSSProperties, FormEvent, ReactNode } from 'react';
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
  getTaskRunDetails,
  getTaskRunEvents,
  overrideTaskStageCheck,
  resumeRun,
  resumeRunWithRuntimeUpgrade,
  retryDelivery,
  returnTaskStage,
  runTaskStageAgent,
  skipTaskStage,
  submitTaskGrillVersion,
  submitTaskStageResult,
} from '../api';
import type { Approval, Artifact, Stage, TaskEvent, TaskResultRevision, TaskRunDetails, WorkflowGraph } from '../types';
import { useWebSocket } from '../hooks/useWebSocket';
import { usePageTitle } from '../hooks/usePageTitle';
import { Bubble } from '../components/Bubble';
import { CopyableHash } from '../components/CopyableHash';
import { StageResultForm } from '../components/StageResultForm';
import { hasHttpPrefix } from '../stageResultValidation';
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

type RouteState = 'passed' | 'current' | 'upcoming' | 'failed';
type AgentLaunch = { phase: 'idle' | 'starting' | 'started'; at: number; previous: string };
const idleLaunch: AgentLaunch = { phase: 'idle', at: 0, previous: '' };
type ActionKey = 'take' | 'run-agent' | 'results' | 'confirm' | 'answer' | 'new-version' | 'done' | 'plan' | 'skip' | 'override'
  | 'return' | 'executor' | 'decision' | 'resume' | 'grant' | 'cancel' | 'delivery';
type ErrorScope = 'stage' | 'control' | 'delivery';
// Where an action error is shown: next to the block that ran the action.
const actionScopes: Record<ActionKey, ErrorScope> = {
  take: 'stage', 'run-agent': 'stage', results: 'stage', confirm: 'stage', answer: 'stage', 'new-version': 'stage',
  done: 'stage', plan: 'stage', skip: 'stage', override: 'stage', return: 'stage', executor: 'stage', decision: 'stage',
  resume: 'control', grant: 'control', cancel: 'control', delivery: 'delivery',
};

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

function httpLink(value: string): boolean {
  if (!hasHttpPrefix(value)) return false;
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
    run_resume_failed: 'Не удалось продолжить задачу',
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
  const [errorScope, setErrorScope] = useState<ErrorScope>('stage');
  const [intentText, setIntentText] = useState<string | null>(null);
  const [ideaOpen, setIdeaOpen] = useState(false);
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
    setApprovalAnswers({});
    setApprovalReasons({});
    setBudgetReason('');
    setAgentLaunch(idleLaunch);
    setResultAccepted(false);
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
  const allowed = useMemo(() => new Set(details?.allowed_actions ?? []), [details?.allowed_actions]);
  const pending = details ? currentApproval(details) : undefined;
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
  useEffect(() => { setReturnTarget(''); }, [currentStageID]);
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

  const returnOptions = template?.returns.filter((route) => route.from === currentStageID) ?? [];
  const effectiveReturnTarget = returnOptions.some((route) => route.to === returnTarget)
    ? returnTarget : returnOptions[0]?.to ?? '';
  const duration = run?.completed_at
    ? Math.max(0, new Date(run.completed_at).getTime() - new Date(run.started_at).getTime())
    : null;

  const perform = async (label: string, action: () => Promise<unknown>, key: ActionKey): Promise<boolean> => {
    const actionRunID = runID;
    const actionStillTargetsCurrentRun = () => activeRunID.current === actionRunID;
    setActionError('');
    setErrorScope('stage');
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
      setErrorScope(actionScopes[key]);
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
    if (action === 'skip') {
      if (await perform('Этап пропущен', () => skipTaskStage(run.run_id, currentStage.id, skipReason.trim()), 'skip')) setSkipReason('');
    } else if (action === 'override') {
      if (await perform('Проверка обойдена', () => overrideTaskStageCheck(run.run_id, currentStage.id, overrideReason.trim()), 'override')) setOverrideReason('');
    } else {
      if (await perform('Возврат отправлен', () => returnTaskStage(run.run_id, currentStage.id, effectiveReturnTarget, returnReason.trim()), 'return')) setReturnReason('');
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
  const resumable = resumableStatuses.includes(run.status);
  const cancelable = cancelableStatuses.includes(run.status);
  const finished = finishedStatuses.includes(run.status);
  const canAct = (role: string) => !principal || principal.roles.some((heldRole) => heldRole === canonicalApprovalRole(role));
  const attemptArtifacts = (stage: Stage) => visibleSnapshot.artifacts.filter((artifact) =>
    artifact.path.includes(`attempts/${stage.attempt_id}/`));

  // Что произойдёт после подтверждения текущего этапа.
  const confirmTargetID = pending?.targets?.approve_spec ?? pending?.targets?.approve
    ?? nextStageFromGraph(template?.graph, currentStageID)
    ?? template?.stages[template.stages.findIndex((stage) => stage.id === currentStageID) + 1]?.id;
  const confirmHint = currentStage && allowed.has('confirm')
    ? confirmTargetID ? `Что произойдёт после подтверждения: ${stageTitle(confirmTargetID)}`
      : 'Что произойдёт после подтверждения: задача завершится'
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
  // One place for the failure: a resume failure wins over the stage failure it follows.
  // The stage that failed keeps its heading; the newest error (a failed resume, when there is one) is the text.
  const issueSource = showFailure
    ? { heading: failureTitle, text: resumeFailure ? resumeFailureReason : failureReason, attempt: failureAttempt }
    : resumeFailure ? { heading: 'Не удалось продолжить задачу', text: resumeFailureReason, attempt: undefined as Stage | undefined } : null;
  const issue = issueSource ? { ...issueSource, ...classifyIssue(issueSource.text) } : null;


  const route = (template?.stages ?? []).map((stage, index) => {
    const currentIndex = template?.stages.findIndex((item) => item.id === currentStageID) ?? -1;
    let state: RouteState = 'upcoming';
    if (finished || (currentIndex >= 0 && index < currentIndex)) state = 'passed';
    else if (index === currentIndex) state = 'current';
    if (latestAttemptFor(stage.id)?.status === 'failed') state = 'failed';
    return { stage, state, current: index === currentIndex };
  });

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

  function approvalLabel(action: string): string {
    if (action === 'answer_questions') return 'Ответить и продолжить';
    if (action === 'run_agent') return 'Запустить агента';
    if (action === 'refine_agent') return 'Попросить агента доработать';
    if (action === 'approve_spec') return 'Согласовать ТЗ и передать архитектору';
    if (action === 'approve') return 'Согласовать';
    if (action === 'new_version') return 'Доработать результат и проверить снова';
    if (action === 'override') return 'Подтвердить без проверки';
    if (action === 'return') return 'Вернуть этап на доработку';
    if (action === 'reject') return 'Отклонить';
    if (action === 'stop') return 'Остановить';
    if (action.startsWith('return_to_')) return `Вернуть на «${stageTitle(action.slice('return_to_'.length))}»`;
    return action;
  }

  const decisionButtonLabel = (approval: Approval, action: string): string => {
    const target = approval.targets?.[action];
    if (action === 'approve' && target) return `Принять и перейти: ${stageTitle(target)}`;
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

  const canRetryDelivery = (!principal || principal.roles.includes('product_owner') || principal.roles.includes('release_manager')) &&
    (run.status === 'completed' || run.status === 'completed_with_warnings') &&
    (details.delivery.status === 'pending' || details.delivery.status === 'failed');
  const prURL = safePullRequestURL(details.delivery.record?.pr_url);

  const stageState = (() => {
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
  const half = Math.ceil(route.length / 2);
  const routeRows = route.length > 6 ? [route.slice(0, half), route.slice(half)] : [route];
  const rowStarts = routeRows.length > 1 ? [0, half] : [0];

  // "submit" and "approve" of a human input go through the stage result form, not the approvals list.
  // A stage whose result is only an approval has no content to submit, so «Принять» is the decision itself.
  const stageNeedsContent = (stageID: string) => (template?.stages.find((stage) => stage.id === stageID)?.result ?? 'md') !== 'approve';
  const decisionActions = (approval: Approval) => (approval.trigger === 'human_input'
    ? approval.actions.filter((action) => action !== 'submit'
      && !(action === 'approve' && stageNeedsContent(approval.from_stage))
      && (!(currentStage?.agent && allowed.has('run-agent')) || !['run_agent', 'refine_agent'].includes(action)))
    : approval.kind === 'check_decision' ? approval.actions.filter((action) => action !== 'override') : approval.actions);

  const renderDecision = (approval: Approval) => {
    const markdown = payloadMarkdown(approval.payload);
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
      {markdown !== null && <div className={styles.markdown}><ReactMarkdown>{markdown || 'Текст вопроса находится в артефакте этапа.'}</ReactMarkdown></div>}
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
        {approval.required_roles
          .filter((role) => canAct(role))
          .flatMap((role) => decisionActions(approval).map((action) => (
            <button key={`${role}:${action}`} type="button" disabled={working ||
              (action === 'answer_questions' && !answer.trim()) ||
              (requiresReturnReason(approval, action) && !reason.trim())}
            onClick={() => void sendDecision(approval, role, action,
              action === 'answer_questions' ? answer : requiresReturnReason(approval, action) ? reason : undefined)}>
              <BusyLabel busy={working && activeKey === 'decision'} text="Сохраняем…">{decisionButtonLabel(approval, action)}</BusyLabel>
            </button>
          )))}
      </div>
      {approval.targets && Object.keys(approval.targets).length > 0 && (
        <small>Далее: {Object.entries(approval.targets).map(([action, target]) =>
          `${approvalLabel(action)} → ${stageTitle(target)}`).join(' · ')}</small>
      )}
      <details className={styles.approvalDetails}>
        <summary>Подробности</summary>
        <div className={styles.hashes}><span>Хеш решения</span><CopyableHash value={approval.subject_hash} label="хеш решения" /></div>
        {approval.candidate_sha256 && <div className={styles.hashes}><span>Хеш кандидата</span><CopyableHash value={approval.candidate_sha256} label="хеш кандидата" /></div>}
        {approval.artifact_revisions && Object.entries(approval.artifact_revisions).map(([path, revision]) => (
          <code key={path}>{path} · {revision}</code>
        ))}
        {approval.payload != null && <pre><code>{JSON.stringify(approval.payload, null, 2)}</code></pre>}
      </details>
    </article>;
  };

  // The idea block: its first line is the title, the next lines a preview, the whole text sits under «Подробности».
  // Full idea text is mounted only while «Подробности» is open, so the title and preview never repeat in the DOM.
  const ideaBody = (intentText !== null ? stripLeadingHeading(intentText) : (details.task ?? '')).trim();
  const ideaLines = ideaBody.split('\n').map((line) => line.replace(/^#+\s*/, '').trim()).filter(Boolean);
  const ideaTitle = ideaLines[0] ? (ideaLines[0].length > 120 ? `${ideaLines[0].slice(0, 117)}…` : ideaLines[0]) : 'Без названия';
  const ideaPreviewText = ideaLines.slice(1).join(' ');
  const ideaPreview = ideaPreviewText.length > 280 ? `${ideaPreviewText.slice(0, 277)}…` : ideaPreviewText;

  // Одна строка о том, что сделать прямо сейчас; подробности — в блоках ниже.
  // A pending result decision (approve / decline) is the one action left: start-work cards would only duplicate it.
  const choiceOwnsStage = Boolean(currentDecision && currentDecision.trigger !== 'human_input');
  const nextStep = (() => {
    if (issue) return null;
    if (currentDecision?.kind === 'check_decision') return 'Проверка не прошла или не завершилась. Откройте отчёт и отправьте результат на повторную проверку.';
    if (currentDecision) return 'Результат ждёт вашего решения: проверьте его и выберите действие ниже.';
    if (runningAttempt) return 'Агент работает. Страница обновится, когда он закончит.';
    if (currentStage?.agent && allowed.has('run-agent')) return `Запустите агента ${currentStage.agent}: он подготовит результат, потом вы его проверите.`;
    if (allowed.has('results')) return 'Сдайте результат текстом или ссылкой.';
    if (allowed.has('confirm')) return 'Результат принят. Подтвердите этап, чтобы перейти дальше.';
    return null;
  })();

  return (
    <main className={styles.container}>
      <div className={styles.layout}>
        <div className={styles.summary}>
          <section className={`${styles.intent} ${styles.ideaCard}`} aria-labelledby="idea-heading">
            <div className={styles.ideaTop}>
              <h2 id="idea-heading">Бизнес-идея</h2>
            </div>
            <p className={styles.ideaTitle}>{ideaTitle}</p>
            {ideaPreview && <p className={styles.ideaPreview}>{ideaPreview}</p>}
            {(details.external_links ?? []).length > 0 && (
              <ul className={styles.externalLinks} aria-label="Внешние ссылки">
                {details.external_links?.map((value) => (
                  <li key={value}>{httpLink(value)
                    ? <a href={value} target="_blank" rel="noreferrer">{value}</a>
                    : <code>{value}</code>}</li>
                ))}
              </ul>
              )}
            <details className={styles.details} onToggle={(event) => setIdeaOpen(event.currentTarget.open)}>
              <summary>Подробности</summary>
              {ideaOpen && (ideaBody
                ? <div className={styles.markdown}><ReactMarkdown>{ideaBody}</ReactMarkdown></div>
                : <p>Текст идеи пока не записан.</p>)}
              <dl>
                <div><dt>ID задачи</dt><dd><CopyableHash value={run.run_id} label="ID задачи" /></dd></div>
                <div><dt>Фича</dt><dd><code>{run.feature}</code></dd></div>
                <div><dt>Режим</dt><dd>{details.mode === 'auto' ? 'Авто' : 'Вручную'}</dd></div>
                <div><dt>Создана</dt><dd>{new Date(run.started_at).toLocaleString('ru-RU')}</dd></div>
                <div><dt>Длительность</dt><dd>{duration !== null ? `${(duration / 1000).toFixed(1)} с` : '—'}</dd></div>
                {template && <div><dt>Шаблон</dt><dd>{template.title || template.template} · <code title={template.version}>{template.version.slice(0, 12)}…</code></dd></div>}
              </dl>
            </details>
          </section>
        </div>

        <div className={styles.stage}>
          <section id="current-stage" className={`${styles.panel} ${styles.stageCard}`} aria-labelledby="current-stage-heading">
            {issue && <IssuePanel runID={run.run_id} issue={issue} working={working} busy={working && activeKey === 'resume'}
              onUpgrade={() => void perform('Задача продолжена на текущей версии', () => resumeRunWithRuntimeUpgrade(run.run_id), 'resume')} />}
            <div className={styles.panelHeading}>
              <div>
                <p className={styles.eyebrow}>Текущий этап</p>
                <h2 id="current-stage-heading">{currentStage?.title || (finished ? 'Задача завершена' : currentStageID || 'Этап не выбран')}</h2>
                <p className={styles.stageState} data-state={stageState}>{stageState}</p>
                {nextStep && <p className={styles.stageGuidance}>{nextStep}</p>}
                {details.stage_owners[currentStageID] && <p>Ответственный: {details.stage_owners[currentStageID].actor_id}
                  {details.stage_owners[currentStageID].taken_at && <> · взял этап <time dateTime={details.stage_owners[currentStageID].taken_at}>
                    {new Date(details.stage_owners[currentStageID].taken_at).toLocaleString('ru-RU')}</time></>}</p>}
                {currentStage && (currentExecutor === 'agent' || agentLaunch.phase !== 'idle') && <AgentStatus runID={run.run_id}
                  runningAttempt={runningAttempt}
                  lastAttempt={lastAgentAttempt} launch={agentLaunch} />}
                {currentStage && currentExecutor !== 'agent' && agentLaunch.phase === 'idle' && runningAttempt && <div className={styles.working} role="status">
                  <span className={styles.spinner} aria-hidden="true" />
                  <p>{`Этап «${currentStage.title}» выполняется, запущено ${clockTime(runningAttempt.started_at)}`}</p>
                </div>}
              </div>
            </div>

            {questionDecisions.length > 0 && <section className={styles.questions} aria-labelledby="question-heading">
              <h3 id="question-heading">Для этапа «{currentStage?.title ?? currentStageID}» нужно уточнение Product Owner</h3>
              {!principal && <label className={styles.questionLabel}>Ваш идентификатор
                <input value={actor} onChange={(event) => setActor(event.target.value)} aria-label="Идентификатор участника" />
              </label>}
              {questionDecisions.some((approval) => payloadMarkdown(approval.payload) === null)
                && <p>Текст вопросов не передан в запросе: откройте артефакт этапа.</p>}
              {questionDecisions.map(renderDecision)}
            </section>}
            {currentDecision && decisionActions(currentDecision).length > 0 && <section className={styles.decisionPanel} aria-labelledby="current-decision-heading">
              <h3 id="current-decision-heading">Твоё решение по этапу</h3>
              {currentDecision.kind === 'check_decision' && <>
                <p>Автоматическая проверка не завершилась или нашла проблему. Открой отчёт и отправь результат на повторную проверку.</p>
                {checkReportPath(currentDecision.payload) && <Link to={briefLink(run.run_id, checkReportPath(currentDecision.payload)!)}>Открыть отчёт проверки</Link>}
              </>}
              {currentOutputArtifacts.length > 0 && <p><a href="#current-result-heading">Посмотреть результат этапа ниже</a></p>}
              {renderDecision(currentDecision)}
              {currentDecision.kind === 'check_decision' && allowed.has('override') && <details className={styles.moreActions}>
                <summary>Дополнительное действие: обойти проверку</summary>
                <form className={styles.reasonForm} onSubmit={submitReason('override')}>
                  <label>Причина обхода проверки
                    <input value={overrideReason} onChange={(event) => setOverrideReason(event.target.value)} required maxLength={4000} />
                  </label>
                  <button disabled={working || !overrideReason.trim()}>Подтвердить без проверки</button>
                </form>
              </details>}
            </section>}

            {(currentOutputArtifacts.length > 0 || (details.results[currentStageID] ?? []).length > 0) &&
              <section className={styles.currentResult} aria-labelledby="current-result-heading">
                <div className={styles.resultHeading}>
                  <h3 id="current-result-heading">Результат этапа</h3>
                  {lastAgentAttempt && <span>Агент завершил работу · попытка {lastAgentAttempt.stage_index}</span>}
                </div>
                <p>{currentDecision ? 'Результат ждёт вашего решения: откройте файл, затем примите его или отправьте на доработку.' : 'Результат сохранён. Следующий шаг — в блоке действий ниже.'}</p>
                {primaryResultPath ? <>
                  <div className={styles.resultFiles}>
                    {currentOutputArtifacts.map((artifact) => <Link key={artifact.path} to={briefLink(run.run_id, artifact.path)}>
                      Открыть {artifact.name}
                    </Link>)}
                  </div>
                </> : (details.results[currentStageID] ?? []).at(-1)?.result === 'link' &&
                  <a href={(details.results[currentStageID] ?? []).at(-1)?.content} target="_blank" rel="noreferrer">Открыть результат</a>}
              </section>}

            {briefArtifacts.length > 1 && <details className={styles.briefHistory}>
              <summary>Ответы и уточнения ({briefArtifacts.length - 1})</summary>
              <div className={styles.resultFiles}>{briefArtifacts.slice(1).map((artifact, index) =>
                <Link key={artifact.path} to={briefLink(run.run_id, artifact.path)}>Уточнение {index + 1}</Link>)}</div>
            </details>}

            {actionStatus && <p className={styles.actionStatus} role="status">{actionStatus}</p>}
            {working && <p className={styles.waiting} role="status"><span className={styles.buttonSpinner} aria-hidden="true" />Ждём ответ сервера…</p>}
            {actionError && errorScope === 'stage' && <p className={styles.actionError} role="alert">{actionError}</p>}

            {currentStage && (allowed.has('take') || allowed.has('results') || allowed.has('run-agent')) && !choiceOwnsStage && <div className={styles.modes}>
              {allowed.has('results') && <article className={styles.modeCard}>
                <h3>Сделаю сам</h3>
                <div className={styles.actions}>
                  {allowed.has('results') && !showResultForm && <button type="button" disabled={working}
                    onClick={() => setShowResultForm(true)}>Сдать результат</button>}
                </div>
              </article>}
              {allowed.has('run-agent') && currentStage.agent && <article className={styles.modeCard}>
                <h3>Агент {currentStage.agent} подготовит результат</h3>
                <div className={styles.actions}>
                  <button type="button" disabled={working}
                    onClick={() => void startAgent(pending?.actions.includes('retry') ? 'refine' : 'run')}>
                    <BusyLabel busy={working && activeKey === 'run-agent'} text="Запускаем…">{pending?.actions.includes('retry') ? 'Попросить агента доработать' : 'Запустить агента'}</BusyLabel>
                  </button>
                </div>
              </article>}
            </div>}

            {currentStage && allowed.size > 0 && <div className={styles.actions}>
              {allowed.has('confirm') && <button type="button" disabled={working}
                onClick={() => void perform('Этап подтверждён', () => confirmTaskStage(run.run_id, currentStage.id), 'confirm')}><BusyLabel busy={working && activeKey === 'confirm'} text="Сохраняем…">{confirmTargetID ? `Подтвердить и перейти: ${stageTitle(confirmTargetID)}` : 'Подтвердить и завершить задачу'}</BusyLabel></button>}
              {(allowed.has('grill/answer') || allowed.has('grill/new-version') || allowed.has('grill/done')) && pending &&
                <GrillPanel approval={pending} answer={grillAnswer} setAnswer={setGrillAnswer} disabled={working} activeKey={activeKey}
                canAnswer={allowed.has('grill/answer')} canNewVersion={allowed.has('grill/new-version')} canDone={allowed.has('grill/done')}
                onAnswer={() => void perform('Ответ отправлен', () => answerTaskGrill(run.run_id, currentStage.id, grillAnswer.trim()), 'answer')}
                onNewVersion={() => void perform('Новая версия отправлена', () =>
                  submitTaskGrillVersion(run.run_id, currentStage.id, grillAnswer.trim()), 'new-version')}
                onDone={() => void perform('Грилл завершён', () => finishTaskGrill(run.run_id, currentStage.id), 'done')} />}
              {allowed.has('plan/approve') && pending?.trigger === 'delivery_plan' && <div className={styles.planApproval}>
                <p>План доставки ожидает подтверждения. Хеш: <code>{pending.subject_hash}</code></p>
                <DeliveryPlanPayload payload={pending.payload} />
                <button type="button" disabled={working} onClick={() => void perform('План подтверждён', () =>
                  approveTaskDeliveryPlan(run.run_id, pending.subject_hash), 'plan')}><BusyLabel busy={working && activeKey === 'plan'} text="Сохраняем…">Подтвердить план</BusyLabel></button>
              </div>}
            </div>}

            {resultAccepted && <p className={styles.resultAccepted} role="status">Результат принят</p>}
        {confirmHint && <p className={styles.confirmHint}>{confirmHint}</p>}

            {currentStage && showResultForm && allowed.has('results') && (
              <StageResultForm key={`${run.run_id}:${currentStage.id}`} runId={run.run_id} stage={currentStage}
                working={working} onSubmit={(value) => void submitResult(value)} onCancel={() => setShowResultForm(false)} />
            )}

            {currentStage && allowed.size > 0 && (allowed.has('skip') || allowed.has('override') || (allowed.has('return') && returnOptions.length > 0)) && (
              <details className={styles.moreActions}>
                <summary>Другие действия: пропуск, обход проверки, возврат</summary>
                {allowed.has('skip') && <form className={styles.reasonForm} onSubmit={submitReason('skip')}>
                  <label>Причина пропуска
                    <input value={skipReason} onChange={(event) => setSkipReason(event.target.value)} required maxLength={4000} />
                  </label>
                  <button disabled={working || !skipReason.trim()}>Пропустить этап</button>
                </form>}
                {allowed.has('override') && currentDecision?.kind !== 'check_decision' && <form className={styles.reasonForm} onSubmit={submitReason('override')}>
                  <label>Причина обхода проверки
                    <input value={overrideReason} onChange={(event) => setOverrideReason(event.target.value)} required maxLength={4000} />
                  </label>
                  <button disabled={working || !overrideReason.trim()}>Обойти проверку</button>
                </form>}
                {allowed.has('return') && returnOptions.length > 0 && <form className={styles.reasonForm} onSubmit={submitReason('return')}>
                  <label>Вернуть на этап
                    <select value={effectiveReturnTarget} onChange={(event) => setReturnTarget(event.target.value)}>
                      {returnOptions.map((route) => <option key={route.to} value={route.to}>
                        {stageTitle(route.to)}
                      </option>)}
                    </select>
                  </label>
                  <label>Причина возврата
                    <input value={returnReason} onChange={(event) => setReturnReason(event.target.value)} required maxLength={4000} />
                  </label>
                  <button disabled={working || !returnReason.trim()}>Вернуть этап</button>
                </form>}
              </details>
            )}
            {/* Слот под текущим этапом: сюда встаёт блок вопросов агента (AgentQuestions). Не удалять. */}
          </section>
        </div>
      </div>

      <div className={styles.fullWidth}>
        <section className={`${styles.panel} ${styles.routePanel}`} aria-labelledby="route-heading">
          <h2 id="route-heading">Маршрут процесса</h2>
          {route.length === 0 ? <p>Этапов нет.</p> : <div className={styles.rails}>
            {routeRows.map((row, rowIndex) => (
              <ol key={rowIndex} className={`${styles.routeRow} ${rowIndex === 0 ? styles.rowTop : styles.rowBottom}`}
                style={{ '--cols': row.length } as CSSProperties}>
                {row.map(({ stage, state, current }, index) => (
                  <li key={stage.id} className={styles.routeItem} data-state={state}
                    data-turn={rowIndex === 0 && routeRows.length > 1 && index === row.length - 1 ? 'true' : undefined}
                    aria-current={current ? 'step' : undefined}>
                    <span className={styles.routeDot} aria-hidden="true">
                      {state === 'passed' ? '✓' : state === 'failed' ? '!' : rowStarts[rowIndex] + index + 1}
                    </span>
                    <span className={styles.routeText}>
                      <a className={styles.routeTitle} href={current ? '#current-stage' : `#result-${stage.id}`}>
                        {stage.title}
                      </a>
                      <span className={styles.routeState}>{routeLabels[state]}</span>
                    </span>
                  </li>
                ))}
              </ol>
            ))}
          </div>}
        </section>

        <section className={`${styles.panel} ${styles.stageRest}`} aria-labelledby="approvals-heading">
          <h2 id="approvals-heading">Согласования</h2>
          {!principal && otherDecisions.length > 0 && <label className={styles.questionLabel}>Ваш идентификатор
            <input value={actor} onChange={(event) => setActor(event.target.value)} aria-label="Идентификатор участника" />
          </label>}
          {otherDecisions.length === 0 && <p>{currentDecision || questionDecisions.length > 0
          ? 'Текущее решение находится в карточке этапа выше.'
          : 'Сейчас никто не ждёт вашего решения.'}</p>}
          {otherDecisions.map(renderDecision)}
          {decidedDecisions.length > 0 && <details className={styles.history}>
            <summary>История согласований ({decidedDecisions.length})</summary>
            {decidedDecisions.map((approval) => <article key={approval.id} className={styles.approval}>
              <strong>{stageTitle(approval.from_stage)}{approval.to_stage !== approval.from_stage ? ` → ${stageTitle(approval.to_stage)}` : ''}</strong>
              <span>решено: {approval.resolved_action || 'решение принято'}
                {approval.resolved_at && <> · <time dateTime={approval.resolved_at}>{new Date(approval.resolved_at).toLocaleString('ru-RU')}</time></>}</span>
              {approval.decisions?.map((decision, index) => <small key={`${decision.actor_id}-${index}`}>
                {decision.actor_id} ({decision.actor_role}): {decision.action}{decision.comment ? ` — ${decision.comment}` : ''}
              </small>)}
            </article>)}
          </details>}
        </section>

        {details.delivery.status !== 'not_requested' && (
          <section className={`${styles.panel} ${styles.stageRest}`} aria-labelledby="delivery-heading">
            <h2 id="delivery-heading">Git-доставка</h2>
          {actionError && errorScope === 'delivery' && <p className={styles.actionError} role="alert">{actionError}</p>}
            <p aria-live="polite">Статус: {details.delivery.status}</p>
            {details.delivery.record && (
              <div>
                {details.delivery.record.commit_sha && <p>Коммит: <code>{details.delivery.record.commit_sha}</code></p>}
                {details.delivery.record.pr_url && <p>Pull request: {prURL
                  ? <a href={prURL} target="_blank" rel="noreferrer">{details.delivery.record.pr_url}</a>
                  : <code>{details.delivery.record.pr_url}</code>}</p>}
              </div>
            )}
            {details.delivery.error && <p role="status">Ошибка доставки: {details.delivery.error}</p>}
            {canRetryDelivery && (
              <div className={styles.actions}>
                <button type="button" disabled={working}
                  onClick={() => void perform('Доставка повторена', () => retryDelivery(run.run_id), 'delivery')}>
                  <BusyLabel busy={working && activeKey === 'delivery'} text="Запускаем…">Повторить одобренную Git-доставку</BusyLabel>
                </button>
              </div>
            )}
          </section>
        )}

        {(resumable || cancelable) && (
          <section className={`${styles.panel} ${styles.stageRest}`} aria-labelledby="control-heading">
            <h2 id="control-heading">Управление задачей</h2>
          <p className={styles.sectionNote}>Продолжить остановленную задачу, выдать бюджет или отменить её. Повтор Git-доставки — в блоке «Git-доставка».</p>
          {actionError && errorScope === 'control' && <p className={styles.actionError} role="alert">{actionError}</p>}
            {resumable && <div className={styles.controlBlock}>
              <p>«Продолжить задачу» запускает её снова с закреплённой версией шаблона и с текущего места.</p>
              <div className={styles.actions}>
                <button type="button" disabled={working} onClick={() => void sendRunCommand('resume')}><BusyLabel busy={working && activeKey === 'resume'} text="Запускаем…">Продолжить задачу</BusyLabel></button>
              </div>
              {run.status === 'stopped' && budgetGrantable && (
                <div className={styles.budgetGrant}>
                  <p>
                    Задачу остановил бюджет задачи. «Продолжить задачу» новый бюджет не выдаёт — задача снова
                    остановится. Выдать ещё один бюджет того же размера можно только явным решением с причиной.
                    Дневной бюджет не выдаётся.
                  </p>
                  <input value={budgetReason} onChange={(event) => setBudgetReason(event.target.value)}
                    aria-label="Причина выдачи бюджета" placeholder="Причина выдачи бюджета" />
                  <button type="button" disabled={working || budgetReason.trim() === ''} onClick={() => void sendRunCommand('grant')}>
                    <BusyLabel busy={working && activeKey === 'grant'} text="Запускаем…">Выдать бюджет и продолжить</BusyLabel>
                  </button>
                </div>
              )}
            </div>}
            {cancelable && <div className={styles.controlBlock}>
              <p>Отмена останавливает выполнение задачи.</p>
              <div className={styles.actions}>
                <button type="button" disabled={working} onClick={() => void sendRunCommand('cancel')}><BusyLabel busy={working && activeKey === 'cancel'} text="Сохраняем…">Отменить задачу</BusyLabel></button>
              </div>
            </div>}
          </section>
        )}
      </div>

      <section className={styles.panel} aria-labelledby="results-heading">
        <h2 id="results-heading">Результаты этапов</h2>
        <div className={styles.resultStages}>
          {(template?.stages ?? []).map((stage) => {
            const attempt = latestAttemptFor(stage.id);
            const files = attempt ? visibleSnapshot.artifacts.filter((artifact) =>
              artifact.run_id === run.run_id && isStageResultArtifact(artifact, attempt.attempt_id)) : [];
            return <ResultHistory key={stage.id} title={stage.title} stageID={stage.id}
              revisions={details.results[stage.id] ?? []} artifacts={files} runID={run.run_id} />;
          })}
        </div>
      </section>

      <section className={styles.panel} aria-labelledby="grill-history-heading">
        <h2 id="grill-history-heading">История грилла</h2>
        <GrillHistory approvals={details.approvals.filter((approval) =>
          payloadMarkdown(approval.payload) !== null && approval.status !== 'pending')}
        />
      </section>

      <details className={styles.techGroup}>
        <summary className={styles.techSummary}>Журнал задачи: таймлайн, этапы, логи и лента событий</summary>
      <section className={styles.panel} aria-labelledby="timeline-heading">
        <h2 id="timeline-heading">Таймлайн задачи</h2>
        {questionDecisions.length > 0 && <p className={styles.questionHint}>Ждём ответ на вопросы · <a href="#current-stage">перейти к карточке</a></p>}
        <TaskTimeline details={details} events={visibleSnapshot.events} eventTitle={eventTitle} />
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
      </details>
    </main>
  );
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


function BusyLabel({ busy, text, children }: { busy: boolean; text: string; children: ReactNode }) {
  if (!busy) return <>{children}</>;
  return <><span className={styles.buttonSpinner} aria-hidden="true" />{text}</>;
}

function IssuePanel({ runID, issue, working, busy, onUpgrade }: {
  runID: string;
  issue: { heading: string; text: string; code: IssueCode; explanation: string; attempt?: Stage };
  working: boolean;
  busy: boolean;
  onUpgrade: () => void;
}) {
  const attempt = issue.attempt;
  return <div className={styles.failure} role="status" data-testid="stage-failure">
    <div className={styles.failureHead}>
      <Bubble tone="coral" size="sm">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round"><path d="M12 7v6M12 17h.01" /></svg>
      </Bubble>
      <p className={styles.failureTitle}>{issue.heading}</p>
    </div>
    <p>{issue.explanation}</p>
    {issue.code === 'version' && <div className={styles.actions}>
      <button type="button" disabled={working} onClick={onUpgrade}>
        <BusyLabel busy={busy} text="Запускаем…">Продолжить на текущей версии</BusyLabel>
      </button>
    </div>}
    <details className={styles.details}>
      <summary>Подробности</summary>
      <p>{issue.text}</p>
      {attempt && <a href={`/api/runs/${encodeURIComponent(runID)}/logs/${encodeURIComponent(attempt.attempt_id)}`}
        target="_blank" rel="noreferrer">Открыть лог агента</a>}
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
  const tone = state === 'finished' ? 'mint' : state === 'running' ? 'yellow' : 'mustard';
  return <div className={styles.agentStatus} role="status" data-state={state}>
    {state === 'running'
      ? <span className={styles.spinner} aria-hidden="true" />
      : <Bubble tone={tone} size="sm">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round">
          <path d={state === 'finished' ? 'M6 12.5l4 4 8-9' : 'M6 12h12M13 7l5 5-5 5'} />
        </svg>
      </Bubble>}
    <div>
      <p className={styles.agentTitle}>{title}</p>
      {attempt && <a href={`/api/runs/${encodeURIComponent(runID)}/logs/${encodeURIComponent(attempt.attempt_id)}`}
        target="_blank" rel="noreferrer">Открыть лог агента</a>}
    </div>
  </div>;
}

function GrillPanel({ approval, answer, setAnswer, disabled, canAnswer, canNewVersion, canDone, onAnswer, onNewVersion, onDone, activeKey }: {
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
    {(canAnswer || canNewVersion) && <label>{canAnswer ? 'Ваш ответ' : 'Комментарий к новой версии'}
      <textarea value={answer} onChange={(event) => setAnswer(event.target.value)} rows={4} maxLength={16 * 1024} />
    </label>}
    {canNewVersion && <p>Чтобы исправить результат, откройте артефакт этапа и нажмите «Предложить изменение»,
      затем отправьте новую версию на проверку. Текст выше уйдёт как комментарий к ней.</p>}
    <div className={styles.actions}>
      {canAnswer && <button type="button" disabled={disabled || !answer.trim()} onClick={onAnswer}><BusyLabel busy={disabled && activeKey === 'answer'} text="Отправляем…">Ответить и продолжить</BusyLabel></button>}
      {canNewVersion && <button type="button" disabled={disabled} onClick={onNewVersion}><BusyLabel busy={disabled && activeKey === 'new-version'} text="Отправляем…">Отправить новую версию</BusyLabel></button>}
      {canDone && <button type="button" disabled={disabled} onClick={onDone}><BusyLabel busy={disabled && activeKey === 'done'} text="Сохраняем…">Завершить грилл без ответа</BusyLabel></button>}
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

function ResultHistory({ title, stageID, revisions, artifacts, runID }: {
  title: string;
  stageID: string;
  revisions: TaskResultRevision[];
  artifacts: Artifact[];
  runID: string;
}) {
  const previous = revisions.length > 1 ? revisions[revisions.length - 2] : null;
  const latest = revisions.at(-1);
  return <article id={`result-${stageID}`} className={styles.resultStage}>
    <h3>{title} <code>{stageID}</code></h3>
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
    return <p role="alert">Содержимое плана недоступно: перед подтверждением проверьте план через CLI.</p>;
  }
  const plan = typeof payload === 'object' && !Array.isArray(payload) ? payload as Record<string, unknown> : {};
  const text = (key: string) => (typeof plan[key] === 'string' ? plan[key] as string : '');
  const files = Array.isArray(plan.files) ? plan.files.filter((file): file is string => typeof file === 'string') : [];
  const fields = deliveryPlanFields.filter(([key]) => text(key));
  const empty = fields.length === 0 && files.length === 0 && !text('pr_body');
  return (
    <section className={styles.planContent} aria-label="Содержимое плана доставки">
      {empty && <p role="alert">В плане нет ни ветки, ни файлов, ни описания PR: перед подтверждением проверьте план через CLI.</p>}
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
