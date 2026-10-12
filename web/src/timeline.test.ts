import { describe, expect, it } from 'vitest';
import { buildTaskTimeline, timelinePosition } from './timeline';
import type { TaskEvent, TaskStageTemplate } from './types';

const stages: TaskStageTemplate[] = [
  { id: 'intent', title: 'Идея', function: 'product_owner', result: 'md', executor: 'human' },
  { id: 'analysis', title: 'Анализ', function: 'analyst', result: 'md', executor: 'agent', agent: 'analyst' },
  { id: 'release', title: 'Релиз', function: 'release_manager', result: 'approve', executor: 'human', skippable: true },
];

const base = Date.parse('2026-10-12T09:00:00Z');
const at = (minutes: number) => new Date(base + minutes * 60_000).toISOString();
let cursor = 0;
function event(type: string, minutes: number, fields: Partial<TaskEvent> = {}, data: Record<string, unknown> = {}): TaskEvent {
  cursor += 1;
  return { version: 1, cursor, run_id: 'run-1', sequence: cursor, type, timestamp: at(minutes), data, ...fields };
}

describe('buildTaskTimeline', () => {
  it('returns lanes without bars for an empty task', () => {
    const timeline = buildTaskTimeline({ events: [], templateStages: stages, now: base });

    expect(timeline.lanes.map((lane) => lane.stageId)).toEqual(['intent', 'analysis', 'release']);
    expect(timeline.lanes.every((lane) => !lane.started && lane.segments.length === 0 && lane.marks.length === 0)).toBe(true);
    expect(timeline.start).toBeUndefined();
    expect(timeline.end).toBeUndefined();
  });

  it('builds state segments of a human stage and an agent stage from events', () => {
    const events = [
      event('run_started', 0),
      event('attempt_started', 0, { attempt_id: 'a-intent' }, { agent: 'intent', stage_index: 1 }),
      event('approval_requested', 0, { attempt_id: 'a-intent' }, { from_stage: 'intent', trigger: 'human_input' }),
      event('stage_taken', 5, { stage: 'intent', actor: 'alice' }),
      event('approval_decided', 20, { attempt_id: 'a-intent' }, { from_stage: 'intent', trigger: 'human_input', decisions: [{ actor_id: 'alice' }] }),
      event('attempt_finished', 21, { attempt_id: 'a-intent' }, { stage: 'intent', executor: 'human', actor_id: 'alice', outcome: 'passed' }),
      event('approval_requested', 22, { attempt_id: 'a-intent' }, { from_stage: 'intent', to_stage: 'analysis', trigger: 'transition' }),
      event('approval_decided', 30, { attempt_id: 'a-intent' }, { from_stage: 'intent', trigger: 'transition', decisions: [{ actor_id: 'bob' }] }),
      event('transition_selected', 30, { attempt_id: 'a-intent' }, { from: 'intent', action: 'approve', target: 'analysis' }),
      event('attempt_started', 30, { attempt_id: 'a-analysis' }, { agent: 'analysis', stage_index: 2 }),
      event('attempt_finished', 40, { attempt_id: 'a-analysis' }, { stage: 'analysis', executor: 'agent', outcome: 'passed' }),
      event('approval_requested', 41, { attempt_id: 'a-analysis' }, { from_stage: 'analysis', trigger: 'grill_questions' }),
      event('approval_decided', 45, { attempt_id: 'a-analysis' }, { from_stage: 'analysis', trigger: 'grill_questions' }),
      event('transition_selected', 46, { attempt_id: 'a-analysis' }, { from: 'analysis', action: 'approve', target: 'release' }),
    ];

    const timeline = buildTaskTimeline({ events, templateStages: stages, now: base + 60 * 60_000 });
    const [intent, analysis, release] = timeline.lanes;

    expect(intent.segments.map((segment) => [segment.state, segment.executor, segment.end - segment.start])).toEqual([
      ['waiting_executor', 'human', 5 * 60_000],
      ['working', 'human', 16 * 60_000],
      ['checking', 'human', 60_000],
      ['awaiting_confirmation', 'human', 8 * 60_000],
    ]);
    expect(intent.actors).toEqual(['alice', 'bob']);
    expect(intent.lastOutcome).toBe('passed');
    expect(intent.durationByState.working).toBe(16 * 60_000);

    expect(analysis.segments.map((segment) => [segment.state, segment.executor])).toEqual([
      ['working', 'agent'],
      ['checking', 'agent'],
    ]);
    expect(analysis.segments[1].end - analysis.segments[1].start).toBe(6 * 60_000);
    expect(analysis.checkRounds).toBe(1);
    expect(analysis.attemptIds).toEqual(['a-analysis']);

    expect(release.started).toBe(false);
    expect(release.segments).toEqual([]);
    expect(timeline.start).toBe(base);
    expect(timeline.end).toBe(base + 46 * 60_000);
  });

  it('marks returns, overrides and skips on the issuing stage', () => {
    const events = [
      event('attempt_started', 0, { attempt_id: 'a-analysis' }, { agent: 'analysis' }),
      event('check_overridden', 5, { stage: 'analysis', attempt_id: 'a-analysis', actor: 'carol' }, { reason: 'flaky check' }),
      event('stage_returned', 6, { stage: 'analysis', attempt_id: 'a-analysis' }, { to_stage: 'intent', reason: 'unclear' }),
      event('transition_selected', 6, { attempt_id: 'a-analysis' }, { from: 'analysis', action: 'return_to_intent', target: 'intent' }),
      event('stage_skipped', 7, { stage: 'release', attempt_id: 'a-release' }, { reason: 'no release needed' }),
    ];

    const timeline = buildTaskTimeline({ events, templateStages: stages, now: base + 10 * 60_000 });
    const analysis = timeline.lanes.find((lane) => lane.stageId === 'analysis')!;
    const release = timeline.lanes.find((lane) => lane.stageId === 'release')!;

    expect(analysis.marks.map((mark) => [mark.kind, mark.target ?? mark.reason])).toEqual([
      ['override', 'flaky check'],
      ['return', 'intent'],
    ]);
    expect(analysis.marks[0].actor).toBe('carol');
    expect(analysis.segments).toHaveLength(1);
    expect(analysis.segments[0].open).toBe(false);
    expect(release.started).toBe(true);
    expect(release.segments).toEqual([]);
    expect(release.marks).toEqual([expect.objectContaining({ kind: 'skip', reason: 'no release needed' })]);
    expect(release.lastOutcome).toBe('skipped');
  });

  it('keeps an unfinished stage open up to now and closes it when the task is finished', () => {
    const events = [event('attempt_started', 0, { attempt_id: 'a-analysis' }, { agent: 'analysis' })];
    const now = base + 15 * 60_000;

    const running = buildTaskTimeline({ events, templateStages: stages, now });
    const open = running.lanes[1].segments[0];
    expect(open).toMatchObject({ state: 'working', executor: 'agent', open: true, start: base, end: now });

    const finished = buildTaskTimeline({ events, templateStages: stages, now, finished: true });
    expect(finished.lanes[1].segments[0].open).toBe(false);
  });

  it('uses the current executor override for an attempt without a finished event', () => {
    const events = [event('attempt_started', 0, { attempt_id: 'a-analysis' }, { agent: 'analysis' })];
    const timeline = buildTaskTimeline({
      events, templateStages: stages, now: base + 60_000, executorOverrides: { analysis: { executor: 'human' } },
    });
    expect(timeline.lanes[1].segments[0]).toMatchObject({ state: 'waiting_executor', executor: 'human' });
  });

  it('ignores unknown event types and malformed data without failing', () => {
    const events = [
      event('attempt_started', 0, { attempt_id: 'a-intent' }, { agent: 'intent' }),
      event('future_event_type', 1, { attempt_id: 'a-intent' }, { anything: [1, 2, 3] }),
      { ...event('approval_requested', 2, { attempt_id: 'a-intent' }), data: null as unknown as Record<string, unknown> },
      { ...event('stage_taken', 3, { stage: 'intent' }), timestamp: 'not a date' },
      event('another_unknown', 4),
    ];

    const timeline = buildTaskTimeline({ events, templateStages: stages, now: base + 5 * 60_000 });
    const intent = timeline.lanes[0];
    expect(intent.eventCursors).toHaveLength(3);
    expect(intent.segments.map((segment) => segment.state)).toEqual(['waiting_executor', 'awaiting_confirmation']);
  });

  it('adds a lane for an event stage missing from the pinned template', () => {
    const events = [event('attempt_started', 0, { attempt_id: 'a-legacy' }, { agent: 'legacy' })];
    const timeline = buildTaskTimeline({ events, templateStages: stages, now: base + 60_000 });
    expect(timeline.lanes.at(-1)).toMatchObject({ stageId: 'legacy', title: 'legacy', started: true });
    expect(timeline.lanes.at(-1)!.segments[0].executor).toBe('unknown');
  });

  it('closes every open segment when the run is canceled', () => {
    const events = [
      event('attempt_started', 0, { attempt_id: 'a-analysis' }, { agent: 'analysis' }),
      event('run_canceled', 3),
    ];
    const timeline = buildTaskTimeline({ events, templateStages: stages, now: base + 60 * 60_000 });
    expect(timeline.lanes[1].segments).toEqual([expect.objectContaining({ open: false, end: base + 3 * 60_000 })]);
  });
});

describe('timelinePosition', () => {
  it('clamps to the scale and handles a zero-length scale', () => {
    expect(timelinePosition(5, 0, 10)).toBe(50);
    expect(timelinePosition(-5, 0, 10)).toBe(0);
    expect(timelinePosition(15, 0, 10)).toBe(100);
    expect(timelinePosition(5, 5, 5)).toBe(0);
  });
});
