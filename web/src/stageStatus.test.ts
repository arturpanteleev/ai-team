import { describe, expect, it } from 'vitest';
import {
  blockText, executorText, isStageStatus, outcomeText, stageStatusEntry, stageStatusGlyphs, stageStatusLabels, stageStatusTones,
  stageSummaryText, visitText,
} from './stageStatus';
import type { StageStatusKey, StageStatusProjection, StageVisit } from './types';

const keys = Object.keys(stageStatusLabels) as StageStatusKey[];
const titleOf = (id: string) => ({ impl: 'Реализация', review: 'Код-ревью' }[id] ?? id);

describe('stage status vocabulary', () => {
  it('names every status the server can send, with a glyph and a tone', () => {
    expect(keys.sort()).toEqual(['awaiting_human', 'blocked', 'canceled', 'checking', 'failed', 'in_progress', 'not_reached', 'passed',
      'ready', 'rejected', 'skipped'].sort());
    for (const key of keys) {
      expect(stageStatusLabels[key]).toBeTruthy();
      expect(stageStatusGlyphs[key]).toBeTruthy();
      expect(stageStatusTones[key]).toBeTruthy();
    }
    expect(new Set(Object.values(stageStatusLabels)).size).toBe(keys.length);
    expect(isStageStatus('checking')).toBe(true);
    expect(isStageStatus('running')).toBe(false);
  });

  it('keeps checking and waiting for a person apart from plain work', () => {
    expect(stageStatusLabels.checking).not.toBe(stageStatusLabels.in_progress);
    expect(stageStatusLabels.awaiting_human).not.toBe(stageStatusLabels.in_progress);
    expect(stageStatusTones.awaiting_human).not.toBe(stageStatusTones.in_progress);
  });

  it('uses red only for real refusals and never amber for a status', () => {
    const red = keys.filter((key) => stageStatusTones[key] === 'danger').sort();
    expect(red).toEqual(['blocked', 'failed', 'rejected']);
    expect(Object.values(stageStatusTones)).not.toContain('amber');
    expect(Object.values(stageStatusTones)).not.toContain('warning');
  });
});

describe('executor text', () => {
  it('tells a person, an internal agent and an external agent apart', () => {
    expect(executorText({ kind: 'human', actor: 'alice', source: 'attempt' })).toBe('Человек: alice');
    expect(executorText({ kind: 'human', source: 'configured' })).toBe('Человек');
    expect(executorText({ kind: 'internal_agent', agent: 'coder', name: 'agentcli', source: 'attempt' })).toBe('Внутренний агент: coder (agentcli)');
    expect(executorText({ kind: 'internal_agent', source: 'configured' })).toBe('Внутренний агент');
    expect(executorText({ kind: 'external_agent', name: 'container-review', source: 'attempt' })).toBe('Внешний агент: container-review');
    expect(executorText({ kind: 'external_agent', agent: 'reviewer', source: 'configured' })).toBe('Внешний агент: reviewer');
    expect(executorText({ kind: 'unknown', source: 'configured' })).toBe('Исполнитель неизвестен');
    expect(executorText({ kind: 'human', source: 'override' })).toBe('Человек · выбран человеком');
    expect(executorText(undefined)).toBe('');
  });
});

describe('visits and summaries', () => {
  const first: StageVisit = {
    number: 1, active: false, entered_at: '2026-10-12T09:00:00Z', entered_from: 'spec', entered_via: 'transition',
    left_at: '2026-10-12T09:10:00Z', left_to: 'review', outcome: 'passed', executor: { kind: 'internal_agent', agent: 'coder', source: 'attempt' },
    attempts: [{ attempt_id: 'a1', executor: { kind: 'internal_agent', source: 'attempt' }, outcome: 'passed', started_at: '2026-10-12T09:00:00Z' }],
  };
  const second: StageVisit = {
    number: 2, active: true, entered_at: '2026-10-12T09:30:00Z', entered_from: 'review', entered_via: 'return', outcome: 'pending',
    executor: { kind: 'internal_agent', agent: 'coder', source: 'configured' }, attempts: [],
  };

  it('says whether a visit is active, how it ended and where it came from', () => {
    expect(visitText(first, titleOf)).toBe('Посещение 1 · неактивно · пройден · Внутренний агент: coder · пришли с «spec» · дальше «Код-ревью» · попыток: 1');
    expect(visitText(second, titleOf)).toBe('Посещение 2 · активно · не завершён · Внутренний агент: coder · возврат с «Код-ревью»');
    expect(visitText({ ...first, returned: true, left_to: 'impl', overridden: true, superseded: true }, titleOf))
      .toContain('вернули на «Реализация» · обход проверки');
    expect(visitText({ ...first, left_to: '$complete', entered_from: undefined }, titleOf)).not.toContain('дальше');
  });

  it('summarizes the current stage with its visit', () => {
    const executor = { kind: 'internal_agent' as const, agent: 'coder', source: 'configured' as const };
    expect(stageSummaryText({ status: 'in_progress', executor, visit_count: 2, active_visit: 2 }, true))
      .toBe('В работе · Внутренний агент: coder · посещение 2 (возврат)');
    expect(stageSummaryText({ status: 'awaiting_human', executor, visit_count: 1, active_visit: 1 }, false)).toBe('Ждёт человека · Внутренний агент: coder');
    expect(stageSummaryText({ status: 'not_reached', executor }, false)).toBe('Не достигнут');
  });

  it('names what blocks the stage', () => {
    expect(blockText({ kind: 'human_input' })).toContain('сдать результат');
    expect(blockText({ kind: 'approval' })).toContain('подтверждение');
    expect(blockText({ kind: 'check_decision' })).toContain('проверка не пройдена');
    expect(blockText({ kind: 'grill_questions' })).toContain('вопросы');
    expect(blockText({ kind: 'delivery_plan' })).toContain('план');
    expect(blockText({ kind: 'gate' })).toContain('гейты');
    expect(blockText({ kind: 'run_state', reason: 'the task is stopped' })).toBe('the task is stopped');
    expect(blockText({ kind: 'other', reason: 'why' })).toBe('why');
    expect(blockText(undefined)).toBe('');
    expect(outcomeText('rejected')).toBe('отклонён');
    expect(outcomeText('novel')).toBe('novel');
  });

  it('finds a stage of the projection', () => {
    const projection = { schema_version: 1, terminal: false, stages: [{ stage_id: 'impl' }] } as unknown as StageStatusProjection;
    expect(stageStatusEntry(projection, 'impl')?.stage_id).toBe('impl');
    expect(stageStatusEntry(projection, 'x')).toBeUndefined();
    expect(stageStatusEntry(undefined, 'impl')).toBeUndefined();
  });
});
