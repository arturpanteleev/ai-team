import { useEffect, useMemo, useRef, useState } from 'react';
import type { KeyboardEvent } from 'react';
import { getTaskFlowMetrics } from '../api';
import { formatStageDuration } from '../duration';
import type { TaskEvent, TaskRunDetails } from '../types';
import type { FlowMetrics, FlowStageStats } from '../types/flowMetrics';
import {
  buildTaskTimeline,
  timelineExecutorLabels,
  timelineMarkLabels,
  timelinePosition,
  timelineStateLabels,
} from '../timeline';
import type { TimelineLane, TimelineMark, TimelineSegment, TimelineState } from '../timeline';
import styles from './TaskTimeline.module.css';

const finishedStatuses = new Set(['completed', 'completed_with_warnings', 'failed', 'canceled', 'stopped', 'interrupted']);
const states: TimelineState[] = ['waiting_executor', 'working', 'checking', 'awaiting_confirmation'];
const markSymbols: Record<TimelineMark['kind'], string> = { return: '↩', override: '!', skip: '»' };

function formatTime(value: number | undefined): string {
  return value === undefined ? 'нет данных' : new Date(value).toLocaleString('ru-RU');
}

function formatTokens(value: number | null | undefined): string {
  return value === null || value === undefined || !Number.isFinite(value)
    ? 'нет данных'
    : new Intl.NumberFormat('ru-RU').format(value);
}

function formatEstimate(value: number | null | undefined, currency?: string): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return 'нет данных';
  try {
    return `≈ ${new Intl.NumberFormat('ru-RU', { style: 'currency', currency: currency || 'USD', maximumFractionDigits: 2 }).format(value)}`;
  } catch {
    return `≈ ${value.toFixed(2)} ${currency || ''}`.trim();
  }
}

function segmentText(segment: TimelineSegment): string {
  return `${timelineStateLabels[segment.state]} (${timelineExecutorLabels[segment.executor]}): ` +
    `${formatTime(segment.start)} — ${segment.open ? 'идёт' : formatTime(segment.end)}, ${formatStageDuration(segment.end - segment.start)}`;
}

function markText(mark: TimelineMark, titleOf: (stageId: string) => string): string {
  return [
    timelineMarkLabels[mark.kind],
    mark.target ? `на этап «${titleOf(mark.target)}»` : '',
    formatTime(mark.at),
    mark.actor ?? '',
    mark.reason ? `причина: ${mark.reason}` : '',
  ].filter(Boolean).join(' · ');
}

function laneSummary(lane: TimelineLane): string {
  if (!lane.started) return `${lane.title}: этап не начат`;
  const parts = states
    .filter((state) => lane.durationByState[state] !== undefined)
    .map((state) => `${timelineStateLabels[state].toLowerCase()} ${formatStageDuration(lane.durationByState[state]!)}`);
  const marks = lane.marks.map((mark) => timelineMarkLabels[mark.kind].toLowerCase());
  return `${lane.title}: ${[...parts, ...marks].join(', ') || 'нет интервалов'}`;
}

export function TaskTimeline({ details, events, eventTitle }: {
  details: TaskRunDetails;
  events: TaskEvent[];
  eventTitle: (type: string) => string;
}) {
  const run = details.run;
  const finished = finishedStatuses.has(run.status);
  const [selected, setSelected] = useState<string | null>(null);
  const [metrics, setMetrics] = useState<FlowMetrics | null>(null);
  const [metricsError, setMetricsError] = useState(false);
  const laneButtons = useRef(new Map<string, HTMLButtonElement>());

  const timeline = useMemo(() => {
    const lastEventAt = events.reduce((latest, event) => Math.max(latest, Date.parse(event.timestamp) || 0), 0);
    const completedAt = run.completed_at ? Date.parse(run.completed_at) : Number.NaN;
    const now = finished ? (Number.isFinite(completedAt) ? completedAt : lastEventAt) : Date.now();
    return buildTaskTimeline({
      events, templateStages: details.template?.stages ?? [], attempts: details.stages,
      executorOverrides: details.state?.executor_overrides, now, finished,
    });
  }, [details.stages, details.state?.executor_overrides, details.template?.stages, events, finished, run.completed_at]);

  // Токены меняются только с завершением попыток — по ним и перезапрашиваем.
  const finishedAttempts = events.filter((event) => event.type === 'attempt_finished').length;
  useEffect(() => {
    let active = true;
    getTaskFlowMetrics(run.run_id)
      .then((value) => { if (active) { setMetrics(value); setMetricsError(false); } })
      .catch(() => { if (active) { setMetrics(null); setMetricsError(true); } });
    return () => { active = false; };
  }, [run.run_id, finishedAttempts]);

  const titleOf = (stageId: string) => timeline.lanes.find((lane) => lane.stageId === stageId)?.title ?? stageId;
  const start = timeline.start ?? 0;
  const end = timeline.end ?? 0;
  const selectedLane = timeline.lanes.find((lane) => lane.stageId === selected);
  const anyStarted = timeline.lanes.some((lane) => lane.started);

  const focusLane = (index: number) => {
    const lane = timeline.lanes[(index + timeline.lanes.length) % timeline.lanes.length];
    if (lane) laneButtons.current.get(lane.stageId)?.focus();
  };
  const onLaneKey = (index: number) => (event: KeyboardEvent<HTMLButtonElement>) => {
    if (event.key === 'ArrowDown') { event.preventDefault(); focusLane(index + 1); }
    if (event.key === 'ArrowUp') { event.preventDefault(); focusLane(index - 1); }
    if (event.key === 'Home') { event.preventDefault(); focusLane(0); }
    if (event.key === 'End') { event.preventDefault(); focusLane(timeline.lanes.length - 1); }
    if (event.key === 'Escape' && selected) { event.preventDefault(); setSelected(null); }
  };
  const closeCard = () => {
    const stageId = selected;
    setSelected(null);
    if (stageId) laneButtons.current.get(stageId)?.focus();
  };

  if (timeline.lanes.length === 0) return <p>В шаблоне задачи нет этапов.</p>;

  return (
    <div className={styles.timeline}>
      <ul className={styles.legend} aria-label="Обозначения таймлайна">
        {states.map((state) => <li key={state}><span className={styles.swatch} data-state={state} data-executor="unknown" aria-hidden="true" />
          {timelineStateLabels[state]}</li>)}
        <li><span className={styles.swatch} data-state="working" data-executor="agent" aria-hidden="true" />Агент — сплошная рамка</li>
        <li><span className={styles.swatch} data-state="working" data-executor="human" aria-hidden="true" />Участник — пунктирная рамка</li>
        {(['return', 'override', 'skip'] as const).map((kind) => <li key={kind}>
          <span className={styles.markSwatch} data-kind={kind} aria-hidden="true">{markSymbols[kind]}</span>{timelineMarkLabels[kind]}
        </li>)}
      </ul>

      {!anyStarted && <p>Этапы ещё не начаты: полос пока нет.</p>}
      {anyStarted && <p className={styles.axis}>
        <span>{formatTime(timeline.start)}</span><span>{finished ? formatTime(timeline.end) : 'сейчас'}</span>
      </p>}

      <ol className={styles.lanes} aria-label="Этапы задачи на шкале времени">
        {timeline.lanes.map((lane, index) => (
          <li key={lane.stageId} className={styles.lane} data-selected={lane.stageId === selected || undefined}>
            <button type="button" className={styles.laneButton}
              ref={(element) => { if (element) laneButtons.current.set(lane.stageId, element); else laneButtons.current.delete(lane.stageId); }}
              aria-expanded={lane.stageId === selected} aria-controls="timeline-stage-card"
              onClick={() => setSelected(lane.stageId === selected ? null : lane.stageId)} onKeyDown={onLaneKey(index)}>
              <span className={styles.laneTitle}>{lane.title}</span>
              <span className={styles.laneMeta}>{lane.started ? `${lane.segments.length} интервал(ов)` : 'не начат'}</span>
            </button>
            <div className={styles.track} role="img" aria-label={laneSummary(lane)}>
              {lane.segments.map((segment, segmentIndex) => {
                const left = timelinePosition(segment.start, start, end);
                const width = Math.max(0.6, timelinePosition(segment.end, start, end) - left);
                return <span key={`${segment.start}-${segmentIndex}`} className={styles.segment}
                  data-state={segment.state} data-executor={segment.executor} data-open={segment.open || undefined}
                  style={{ left: `${left}%`, width: `${Math.min(width, 100 - left)}%` }} title={segmentText(segment)} />;
              })}
              {lane.marks.map((mark) => <span key={`${mark.kind}-${mark.cursor}`} className={styles.mark} data-kind={mark.kind}
                style={{ left: `${timelinePosition(mark.at, start, end)}%` }} title={markText(mark, titleOf)}>{markSymbols[mark.kind]}</span>)}
            </div>
          </li>
        ))}
      </ol>

      {selectedLane && <StageCard lane={selectedLane} details={details} events={events} eventTitle={eventTitle} titleOf={titleOf}
        stats={metrics?.by_stage.find((stage) => stage.stage === selectedLane.stageId)} metrics={metrics} metricsError={metricsError}
        onClose={closeCard} />}

      <details className={styles.textAlternative}>
        <summary>Таймлайн таблицей</summary>
        <table>
          <caption>Интервалы и отметки по этапам</caption>
          <thead><tr><th scope="col">Этап</th><th scope="col">Состояние или отметка</th><th scope="col">Исполнитель</th>
            <th scope="col">Начало</th><th scope="col">Конец</th><th scope="col">Длительность</th></tr></thead>
          <tbody>
            {timeline.lanes.flatMap((lane) => {
              if (!lane.started) return [<tr key={lane.stageId}><th scope="row">{lane.title}</th><td colSpan={5}>Не начат</td></tr>];
              const rows = lane.segments.map((segment, segmentIndex) => <tr key={`${lane.stageId}-s-${segmentIndex}`}>
                <th scope="row">{lane.title}</th><td>{timelineStateLabels[segment.state]}</td>
                <td>{timelineExecutorLabels[segment.executor]}</td><td>{formatTime(segment.start)}</td>
                <td>{segment.open ? 'идёт' : formatTime(segment.end)}</td><td>{formatStageDuration(segment.end - segment.start)}</td>
              </tr>);
              const marks = lane.marks.map((mark) => <tr key={`${lane.stageId}-m-${mark.cursor}`}>
                <th scope="row">{lane.title}</th><td colSpan={5}>{markText(mark, titleOf)}</td>
              </tr>);
              return rows.length + marks.length > 0 ? [...rows, ...marks]
                : [<tr key={lane.stageId}><th scope="row">{lane.title}</th><td colSpan={5}>Есть события, интервалов состояния нет</td></tr>];
            })}
          </tbody>
        </table>
      </details>
    </div>
  );
}

function StageCard({ lane, details, events, eventTitle, titleOf, stats, metrics, metricsError, onClose }: {
  lane: TimelineLane;
  details: TaskRunDetails;
  events: TaskEvent[];
  eventTitle: (type: string) => string;
  titleOf: (stageId: string) => string;
  stats?: FlowStageStats;
  metrics: FlowMetrics | null;
  metricsError: boolean;
  onClose: () => void;
}) {
  const template = details.template?.stages.find((stage) => stage.id === lane.stageId);
  const executors = [...new Set(lane.segments.map((segment) => segment.executor))];
  const owner = details.stage_owners[lane.stageId]?.actor_id;
  const actors = [...new Set([...(owner ? [owner] : []), ...lane.actors])];
  const versions = details.results[lane.stageId] ?? [];
  const laneEvents = events.filter((event) => lane.eventCursors.includes(event.cursor));
  const usageNote = metricsError ? 'нет данных (метрики недоступны)' : !metrics ? 'загружаем…' : '';

  return (
    <section id="timeline-stage-card" className={styles.card} aria-labelledby="timeline-stage-card-heading"
      onKeyDown={(event) => { if (event.key === 'Escape') { event.preventDefault(); onClose(); } }}>
      <div className={styles.cardHeading}>
        <h3 id="timeline-stage-card-heading">Этап «{lane.title}»</h3>
        <button type="button" onClick={onClose}>Закрыть карточку</button>
      </div>
      {!lane.started ? <p>Этап не начат: данных по нему нет.</p> : <dl className={styles.facts}>
        <div><dt>Исполнитель</dt><dd>{executors.length > 0
          ? executors.map((executor) => executor === 'agent' && template?.agent ? `агент ${template.agent}` : timelineExecutorLabels[executor]).join(', ')
          : 'нет данных'}</dd></div>
        <div><dt>Кто делал</dt><dd>{actors.length > 0 ? actors.join(', ') : 'нет данных'}</dd></div>
        <div><dt>Начало и конец</dt><dd>{formatTime(lane.segments[0]?.start ?? lane.firstAt)} — {
          lane.segments.at(-1)?.open ? 'идёт' : formatTime(lane.segments.at(-1)?.end ?? lane.lastAt)}</dd></div>
        <div><dt>Время по состояниям</dt><dd>{states.filter((state) => lane.durationByState[state] !== undefined)
          .map((state) => `${timelineStateLabels[state]}: ${formatStageDuration(lane.durationByState[state]!)}`).join('; ') || 'нет данных'}</dd></div>
        <div><dt>Результат</dt><dd>{lane.lastOutcome ?? 'нет данных'}</dd></div>
        <div><dt>Версии результата</dt><dd>{versions.length > 0
          ? versions.map((version) => `v${version.revision} (${version.actor_id})`).join(', ')
          : 'нет версий'}</dd></div>
        <div><dt>Раунды проверки</dt><dd>{lane.checkRounds}</dd></div>
        <div><dt>Попытки</dt><dd>{lane.attemptIds.length > 0 ? <ul className={styles.inlineList}>
          {lane.attemptIds.map((attemptId) => <li key={attemptId}>
            <a href={`/api/runs/${encodeURIComponent(details.run.run_id)}/logs/${encodeURIComponent(attemptId)}`} target="_blank" rel="noreferrer">
              лог <code>{attemptId}</code></a></li>)}
        </ul> : 'нет данных'}</dd></div>
        <div><dt>Токены (вход / выход)</dt><dd>{usageNote ||
          `${formatTokens(stats?.tokens_input)} / ${formatTokens(stats?.tokens_output)}`}</dd></div>
        <div><dt>Стоимость</dt><dd>{usageNote ||
          `${formatEstimate(stats?.subscription_share_estimate, stats?.subscription_currency)}${
            stats?.subscription_share_estimate != null ? ' — приблизительная доля подписки' : ''}`}</dd></div>
        <div><dt>Возвраты, обходы, пропуски</dt><dd>{lane.marks.length > 0 ? <ul className={styles.inlineList}>
          {lane.marks.map((mark) => <li key={`${mark.kind}-${mark.cursor}`}>{markText(mark, titleOf)}</li>)}
        </ul> : 'нет'}</dd></div>
      </dl>}
      {laneEvents.length > 0 && <>
        <h4>События этапа</h4>
        <ol className={styles.cardEvents}>
          {laneEvents.map((event) => <li key={event.cursor}>
            <a href={`#task-event-${event.cursor}`}>{eventTitle(event.type)}</a>{' '}
            <time dateTime={event.timestamp}>{new Date(event.timestamp).toLocaleString('ru-RU')}</time>
          </li>)}
        </ol>
      </>}
    </section>
  );
}
