// Построение таймлайна задачи (FR-22) из ленты событий задачи — чистая функция,
// без обращений к сети и к текущему времени (время «сейчас» передаётся явно).
//
// Состояние этапа выводится только из записанных событий:
//   attempt_started            → работа (агент) или ожидание исполнителя (участник);
//   approval_requested         → human_input: ожидание исполнителя;
//                                grill_questions / stage_check: проверка;
//                                прочие (подтверждение перехода, план доставки): ожидание подтверждения;
//   stage_taken                → работа участника;
//   approval_decided           → human_input: работа (результат сдан и обрабатывается);
//                                grill_questions / stage_check: проверка продолжается;
//                                прочие: интервал закрывается;
//   attempt_finished           → проверка (проверки этапа и гейты идут после попытки);
//                                неуспешный исход закрывает интервал;
//   transition_selected        → интервал закрывается; действие return_to_* — отметка возврата;
//   stage_skipped              → интервал закрывается, отметка пропуска;
//   check_overridden           → отметка обхода;
//   run_finished, run_canceled → закрывают все интервалы.
// Остальные события (в том числе неизвестные типы) не меняют состояние, но
// попадают в список событий этапа, если их этап удаётся определить.

import type { Stage, TaskEvent, TaskStageTemplate } from './types';

export type TimelineState = 'waiting_executor' | 'working' | 'checking' | 'awaiting_confirmation';
export type TimelineExecutor = 'human' | 'agent' | 'unknown';
export type TimelineMarkKind = 'return' | 'override' | 'skip';

export const timelineStateLabels: Record<TimelineState, string> = {
  waiting_executor: 'Ожидание исполнителя',
  working: 'Работа',
  checking: 'Проверка',
  awaiting_confirmation: 'Ожидание подтверждения',
};

export const timelineExecutorLabels: Record<TimelineExecutor, string> = {
  human: 'участник',
  agent: 'агент',
  unknown: 'исполнитель неизвестен',
};

export const timelineMarkLabels: Record<TimelineMarkKind, string> = {
  return: 'Возврат',
  override: 'Обход проверки',
  skip: 'Пропуск',
};

export interface TimelineSegment {
  state: TimelineState;
  executor: TimelineExecutor;
  start: number;
  end: number;
  // Интервал ещё идёт: конец — переданное «сейчас».
  open: boolean;
  attemptId?: string;
}

export interface TimelineMark {
  kind: TimelineMarkKind;
  at: number;
  cursor: number;
  reason?: string;
  target?: string;
  actor?: string;
}

export interface TimelineLane {
  stageId: string;
  title: string;
  segments: TimelineSegment[];
  marks: TimelineMark[];
  // Этап начат, если по нему есть хоть одно событие или попытка.
  started: boolean;
  actors: string[];
  attemptIds: string[];
  eventCursors: number[];
  checkRounds: number;
  durationByState: Partial<Record<TimelineState, number>>;
  firstAt?: number;
  lastAt?: number;
  lastOutcome?: string;
}

export interface TaskTimeline {
  lanes: TimelineLane[];
  start?: number;
  end?: number;
}

export interface TimelineInput {
  events: TaskEvent[];
  templateStages: TaskStageTemplate[];
  attempts?: Stage[];
  executorOverrides?: Record<string, { executor: 'human' | 'agent' }>;
  // Конец открытых интервалов: «сейчас» для идущей задачи или время завершения.
  now: number;
  // Задача завершена: открытые интервалы не помечаются как идущие.
  finished?: boolean;
}

const checkTriggers = new Set(['grill_questions', 'stage_check']);
const failedOutcomes = new Set(['failed', 'blocked', 'canceled', 'rejected', 'skipped', 'invalidated']);

function text(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function executorKind(value: unknown): TimelineExecutor | '' {
  const kind = text(value).toLowerCase();
  return kind === 'human' || kind === 'agent' ? kind : '';
}

function timestampOf(event: TaskEvent): number {
  const value = Date.parse(event.timestamp);
  return Number.isFinite(value) ? value : Number.NaN;
}

function data(event: TaskEvent): Record<string, unknown> {
  return event.data && typeof event.data === 'object' && !Array.isArray(event.data) ? event.data : {};
}

interface LaneBuilder {
  lane: TimelineLane;
  current?: TimelineSegment;
  returnKeys: Set<string>;
  actors: Set<string>;
  attempts: Set<string>;
}

export function buildTaskTimeline(input: TimelineInput): TaskTimeline {
  const order = input.templateStages.map((stage) => stage.id);
  const templateByID = new Map(input.templateStages.map((stage) => [stage.id, stage]));
  const builders = new Map<string, LaneBuilder>();
  const laneFor = (stageId: string): LaneBuilder => {
    let builder = builders.get(stageId);
    if (!builder) {
      builder = {
        lane: {
          stageId, title: templateByID.get(stageId)?.title || stageId, segments: [], marks: [], started: false,
          actors: [], attemptIds: [], eventCursors: [], checkRounds: 0, durationByState: {},
        },
        returnKeys: new Set(), actors: new Set(), attempts: new Set(),
      };
      builders.set(stageId, builder);
    }
    return builder;
  };
  for (const id of order) laneFor(id);

  const events = input.events
    .filter((event) => Number.isFinite(timestampOf(event)))
    .slice()
    .sort((left, right) => timestampOf(left) - timestampOf(right) || left.cursor - right.cursor);

  // Этап и исполнитель попытки: из истории попыток и из событий попытки.
  const attemptStage = new Map<string, string>();
  const attemptExecutor = new Map<string, TimelineExecutor>();
  for (const attempt of input.attempts ?? []) {
    if (attempt.attempt_id && attempt.agent_name) attemptStage.set(attempt.attempt_id, attempt.agent_name);
  }
  for (const event of events) {
    const payload = data(event);
    if (!event.attempt_id) continue;
    if (event.type === 'attempt_started' && text(payload.agent)) attemptStage.set(event.attempt_id, text(payload.agent));
    if (event.type === 'attempt_finished') {
      const kind = executorKind(event.executor) || executorKind(payload.executor);
      if (kind) attemptExecutor.set(event.attempt_id, kind);
    }
  }

  const stageOf = (event: TaskEvent): string => {
    const payload = data(event);
    return event.stage || text(payload.stage) || text(payload.stage_id) || text(payload.from_stage) ||
      text(payload.from) || (event.type === 'attempt_started' ? text(payload.agent) : '') ||
      (event.attempt_id ? attemptStage.get(event.attempt_id) ?? '' : '');
  };

  const executorOf = (event: TaskEvent, stageId: string): TimelineExecutor => {
    const payload = data(event);
    return executorKind(event.executor) || executorKind(payload.executor) ||
      (event.attempt_id ? attemptExecutor.get(event.attempt_id) : undefined) ||
      input.executorOverrides?.[stageId]?.executor || templateByID.get(stageId)?.executor || 'unknown';
  };

  const close = (builder: LaneBuilder, at: number) => {
    const segment = builder.current;
    if (!segment) return;
    segment.end = Math.max(segment.start, at);
    segment.open = false;
    if (segment.end > segment.start) builder.lane.segments.push(segment);
    builder.current = undefined;
  };

  const enter = (builder: LaneBuilder, state: TimelineState, executor: TimelineExecutor, at: number, attemptId?: string) => {
    const segment = builder.current;
    if (segment && segment.state === state && segment.executor === executor) return;
    close(builder, at);
    builder.current = { state, executor, start: at, end: at, open: true, attemptId };
  };

  for (const event of events) {
    const at = timestampOf(event);
    const payload = data(event);
    if (event.type === 'run_finished' || event.type === 'run_canceled') {
      for (const builder of builders.values()) close(builder, at);
      continue;
    }
    const stageId = stageOf(event);
    if (!stageId) continue;
    const builder = laneFor(stageId);
    const lane = builder.lane;
    lane.started = true;
    lane.eventCursors.push(event.cursor);
    lane.firstAt = lane.firstAt === undefined ? at : Math.min(lane.firstAt, at);
    lane.lastAt = lane.lastAt === undefined ? at : Math.max(lane.lastAt, at);
    if (event.attempt_id) builder.attempts.add(event.attempt_id);
    const actor = event.actor || text(payload.actor_id) || text(payload.actor);
    if (actor && actor !== 'system') builder.actors.add(actor);
    const executor = executorOf(event, stageId);
    const trigger = text(payload.trigger);

    switch (event.type) {
      case 'attempt_started':
        enter(builder, executor === 'human' ? 'waiting_executor' : 'working', executor, at, event.attempt_id);
        break;
      case 'stage_taken':
        enter(builder, 'working', 'human', at, event.attempt_id ?? builder.current?.attemptId);
        break;
      case 'approval_requested':
        if (trigger === 'human_input') {
          enter(builder, 'waiting_executor', executor, at, event.attempt_id);
        } else if (checkTriggers.has(trigger)) {
          lane.checkRounds += 1;
          enter(builder, 'checking', executor, at, event.attempt_id);
        } else {
          enter(builder, 'awaiting_confirmation', executor, at, event.attempt_id);
        }
        break;
      case 'approval_decided':
        if (trigger === 'human_input') {
          enter(builder, 'working', executor, at, event.attempt_id);
        } else if (checkTriggers.has(trigger)) {
          enter(builder, 'checking', executor, at, event.attempt_id);
        } else {
          close(builder, at);
        }
        for (const decision of Array.isArray(payload.decisions) ? payload.decisions : []) {
          const decisionActor = decision && typeof decision === 'object' ? text((decision as Record<string, unknown>).actor_id) : '';
          if (decisionActor) builder.actors.add(decisionActor);
        }
        break;
      case 'attempt_finished': {
        const outcome = text(payload.outcome) || text(payload.status);
        if (outcome) lane.lastOutcome = outcome;
        if (failedOutcomes.has(outcome)) close(builder, at);
        else enter(builder, 'checking', executor, at, event.attempt_id);
        break;
      }
      case 'transition_selected': {
        close(builder, at);
        const action = text(payload.action);
        if (action.startsWith('return_to_')) {
          addReturn(builder, event, at, text(payload.target) || action.slice('return_to_'.length), actor);
        }
        break;
      }
      case 'stage_returned':
      case 'stage_return_requested':
      case 'stage_returned_to':
        addReturn(builder, event, at, text(payload.to_stage) || text(payload.target) || text(payload.to), actor);
        break;
      case 'stage_skipped':
        close(builder, at);
        lane.lastOutcome = 'skipped';
        lane.marks.push({ kind: 'skip', at, cursor: event.cursor, reason: text(payload.reason) || undefined, actor: actor || undefined });
        break;
      case 'check_overridden':
        lane.marks.push({ kind: 'override', at, cursor: event.cursor, reason: text(payload.reason) || undefined, actor: actor || undefined });
        break;
      default:
        break;
    }
  }

  for (const builder of builders.values()) {
    const segment = builder.current;
    if (segment) {
      segment.end = Math.max(segment.start, input.now);
      segment.open = !input.finished;
      builder.lane.segments.push(segment);
      builder.current = undefined;
    }
    const lane = builder.lane;
    for (const attempt of input.attempts ?? []) {
      if (attempt.agent_name !== lane.stageId) continue;
      builder.attempts.add(attempt.attempt_id);
      lane.started = true;
      if (!lane.lastOutcome) lane.lastOutcome = attempt.outcome || attempt.status;
    }
    lane.actors = [...builder.actors];
    lane.attemptIds = [...builder.attempts];
    for (const item of lane.segments) {
      lane.durationByState[item.state] = (lane.durationByState[item.state] ?? 0) + (item.end - item.start);
    }
    lane.marks.sort((left, right) => left.at - right.at || left.cursor - right.cursor);
  }

  const lanes = [
    ...order.map((id) => builders.get(id)!),
    ...[...builders.entries()].filter(([id]) => !templateByID.has(id)).map(([, builder]) => builder),
  ].map((builder) => builder.lane);

  let start: number | undefined;
  let end: number | undefined;
  for (const lane of lanes) {
    for (const segment of lane.segments) {
      start = start === undefined ? segment.start : Math.min(start, segment.start);
      end = end === undefined ? segment.end : Math.max(end, segment.end);
    }
    for (const mark of lane.marks) {
      start = start === undefined ? mark.at : Math.min(start, mark.at);
      end = end === undefined ? mark.at : Math.max(end, mark.at);
    }
  }
  return { lanes, start, end };
}

function addReturn(builder: LaneBuilder, event: TaskEvent, at: number, target: string, actor: string) {
  // Один возврат записывается и переходом, и событием возврата той же попытки.
  const key = event.attempt_id ? `attempt:${event.attempt_id}` : `event:${event.cursor}`;
  if (builder.returnKeys.has(key)) return;
  builder.returnKeys.add(key);
  builder.lane.marks.push({ kind: 'return', at, cursor: event.cursor, target: target || undefined, actor: actor || undefined });
}

// Положение момента на шкале в процентах; при нулевой длине шкалы — 0.
export function timelinePosition(at: number, start: number, end: number): number {
  if (!(end > start)) return 0;
  return Math.min(100, Math.max(0, ((at - start) / (end - start)) * 100));
}
