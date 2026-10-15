import type { GateAttempt, GateExecution, GateVerdict, TaskGates } from './types';

// Pure helpers of the gate views (S06): what an attempt's outcome is called,
// and which marker a stage gets in the route.

export type GateTone = 'pass' | 'fail' | 'technical' | 'neutral';

// Technical execution and content verdict are kept apart: a failed gate, an
// error and a timeout never collapse into one label.
export function outcomeOf(execution: GateExecution | undefined, verdict?: GateVerdict): { label: string; tone: GateTone } {
  switch (execution) {
    case undefined: return { label: 'Не запускался', tone: 'neutral' };
    case 'succeeded':
      if (verdict === 'pass') return { label: 'Пройден', tone: 'pass' };
      if (verdict === 'not_applicable') return { label: 'Не применим', tone: 'neutral' };
      return { label: 'Провал', tone: 'fail' };
    case 'error': return { label: 'Ошибка запуска', tone: 'technical' };
    case 'timeout': return { label: 'Таймаут', tone: 'technical' };
    case 'canceled': return { label: 'Прерван', tone: 'technical' };
    case 'pending': return { label: 'Ждёт решения', tone: 'neutral' };
    default: return { label: 'Выполняется', tone: 'neutral' };
  }
}

export function attemptOutcome(attempt?: GateAttempt) {
  return outcomeOf(attempt?.execution, attempt?.verdict);
}

// The marker of a stage in the route: red for an override of a failure, amber
// for recommendations that did not pass.
export function stageGateFlag(gates: TaskGates | null | undefined, stageId: string): 'override' | 'advisory' | undefined {
  const wave = gates?.stages.find((stage) => stage.stage_id === stageId)?.current;
  if (!wave) return undefined;
  if (wave.override && wave.transition.state === 'overridden') return 'override';
  if (wave.gates.some((gate) => gate.warning)) return 'advisory';
  return undefined;
}
