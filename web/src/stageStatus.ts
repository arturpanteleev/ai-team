// Показ вычисляемых статусов этапов (S11). Сервер выводит статус из журнала,
// здесь только подписи и сборка текста: одни и те же понятия на карточке
// задачи, в маршруте и на таймлайне.

import type {
  StageStatusBlock, StageStatusEntry, StageStatusExecutor, StageStatusKey, StageStatusProjection, StageVisit,
} from './types';

export const stageStatusLabels: Record<StageStatusKey, string> = {
  not_reached: 'Не достигнут',
  ready: 'Готов к работе',
  in_progress: 'В работе',
  checking: 'Проверяется',
  awaiting_human: 'Ждёт человека',
  passed: 'Пройден',
  rejected: 'Отклонён',
  failed: 'Ошибка',
  blocked: 'Заблокирован',
  skipped: 'Пропущен',
  canceled: 'Отменён',
};

// Значок дублирует цвет: статус читается и без него.
export const stageStatusGlyphs: Record<StageStatusKey, string> = {
  not_reached: '○',
  ready: '◔',
  in_progress: '▶',
  checking: '⟳',
  awaiting_human: '⏸',
  passed: '✓',
  rejected: '✕',
  failed: '!',
  blocked: '⛔',
  skipped: '»',
  canceled: '–',
};

// Красный — только реальные отказы и ошибки; янтарного среди статусов нет:
// он остаётся за рекомендациями гейтов.
export type StageStatusTone = 'gray' | 'blue' | 'yellow' | 'teal' | 'green' | 'danger';
export const stageStatusTones: Record<StageStatusKey, StageStatusTone> = {
  not_reached: 'gray',
  ready: 'blue',
  in_progress: 'yellow',
  checking: 'yellow',
  awaiting_human: 'teal',
  passed: 'green',
  rejected: 'danger',
  failed: 'danger',
  blocked: 'danger',
  skipped: 'gray',
  canceled: 'gray',
};

export function isStageStatus(value: unknown): value is StageStatusKey {
  return typeof value === 'string' && value in stageStatusLabels;
}

export const executorKindLabels: Record<string, string> = {
  human: 'Человек',
  internal_agent: 'Внутренний агент',
  external_agent: 'Внешний агент',
  unknown: 'Исполнитель неизвестен',
};

// «Внутренний агент: coder (agentcli)», «Внешний агент: container-review»,
// «Человек: alice».
export function executorText(executor: StageStatusExecutor | undefined): string {
  if (!executor) return '';
  const kind = executorKindLabels[executor.kind] ?? executorKindLabels.unknown;
  let detail = '';
  if (executor.kind === 'human') detail = executor.actor ?? '';
  else if (executor.kind === 'external_agent') detail = executor.name ?? executor.agent ?? '';
  else if (executor.kind === 'internal_agent') {
    detail = executor.agent ?? '';
    if (executor.name) detail = detail ? `${detail} (${executor.name})` : executor.name;
  }
  const text = detail ? `${kind}: ${detail}` : kind;
  if (executor.source === 'override') return `${text} · выбран человеком`;
  return text;
}

const outcomeLabels: Record<string, string> = {
  passed: 'пройден',
  warning: 'пройден с замечанием',
  rejected: 'отклонён',
  failed: 'ошибка',
  blocked: 'заблокирован',
  skipped: 'пропущен',
  canceled: 'отменён',
  invalidated: 'результат устарел',
  pending: 'не завершён',
  none: 'без результата',
};

export function outcomeText(outcome: string): string {
  return outcomeLabels[outcome] ?? outcome;
}

// Источник блокировки: чего именно ждёт текущий этап.
export function blockText(block: StageStatusBlock | undefined): string {
  switch (block?.kind) {
    case 'human_input': return 'нужно взять этап и сдать результат';
    case 'approval': return 'нужно подтверждение результата';
    case 'check_decision': return 'проверка не пройдена: нужно решение';
    case 'grill_questions': return 'нужны ответы на вопросы проверки';
    case 'delivery_plan': return 'нужно утвердить план доставки';
    case 'gate': return 'идут гейты этапа';
    case 'run_state': return block.reason ?? 'задача ждёт возобновления';
    default: return block?.reason ?? '';
  }
}

export function stageStatusEntry(projection: StageStatusProjection | undefined, stageId: string): StageStatusEntry | undefined {
  return projection?.stages.find((stage) => stage.stage_id === stageId);
}

// Одна строка о посещении: номер, активно ли оно, чем кончилось, кто работал,
// откуда пришли и куда ушли.
export function visitText(visit: StageVisit, titleOf: (stageId: string) => string): string {
  const parts = [`Посещение ${visit.number}`, visit.active ? 'активно' : 'неактивно', outcomeText(visit.outcome)];
  if (visit.superseded) parts.push('результат отменён возвратом');
  parts.push(executorText(visit.executor));
  if (visit.entered_via === 'return' && visit.entered_from) parts.push(`возврат с «${titleOf(visit.entered_from)}»`);
  else if (visit.entered_from) parts.push(`пришли с «${titleOf(visit.entered_from)}»`);
  if (visit.left_to && !visit.left_to.startsWith('$')) {
    parts.push(`${visit.returned ? 'вернули на' : 'дальше'} «${titleOf(visit.left_to)}»`);
  }
  if (visit.overridden) parts.push('обход проверки');
  if (visit.attempts.length > 0) parts.push(`попыток: ${visit.attempts.length}`);
  return parts.filter(Boolean).join(' · ');
}

// Сводка текущего этапа: «Ждёт человека · Внутренний агент: coder · посещение 2 (возврат)».
export function stageSummaryText(
  entry: { status: StageStatusKey; executor?: StageStatusExecutor; visit_count?: number; active_visit?: number },
  returned: boolean,
): string {
  const parts = [stageStatusLabels[entry.status] ?? entry.status];
  if (entry.status !== 'not_reached' && entry.executor) parts.push(executorText(entry.executor));
  if ((entry.visit_count ?? 0) > 1 && (entry.active_visit ?? 0) > 0) parts.push(`посещение ${entry.active_visit}${returned ? ' (возврат)' : ''}`);
  return parts.join(' · ');
}
