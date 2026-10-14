import type { TaskRun } from '../types';
import { formatStageDuration } from '../duration';
import { useNavigate } from '../router';
import { shortenHash } from '../hash';
import { statusLabels } from '../statusLabels';
import { taskCardTitle } from '../boardTitle';
import styles from './TaskCard.module.css';

interface TaskCardProps {
  run: TaskRun;
  /** Название шаблона: заголовок карточки, если в брифе нет описания идеи. */
  templateTitle?: string;
  /** Заголовок этапа из шаблона; id этапа, если шаблон недоступен. */
  stageTitle?: (stageId: string) => string;
  /** Текущий момент (мс) для «сколько на этапе»; обновляется тикером страницы. */
  now?: number;
  onCancel?: () => void;
}

type ChipTone = 'yellow' | 'mustard' | 'coral' | 'green' | 'blue' | 'danger' | 'gray';

const statusTone: Record<string, ChipTone> = {
  running: 'yellow',
  waiting_for_approval: 'mustard',
  blocked: 'coral',
  completed: 'green',
  completed_with_warnings: 'green',
  queued: 'blue',
  failed: 'danger',
  canceled: 'gray',
  stopped: 'gray',
  interrupted: 'gray',
};

function executorLabel(run: TaskRun): string {
  if (run.executor === 'agent') return run.executor_agent ? `Агент: ${run.executor_agent}` : 'Агент';
  if (run.executor === 'human') return 'Участник';
  return '';
}

function modeLabel(run: TaskRun): string {
  if (run.mode === 'auto') return 'Авто';
  if (run.mode === 'manual') return 'Вручную';
  return '—';
}

function warningsLabel(count: number): string {
  const mod10 = count % 10;
  const mod100 = count % 100;
  if (mod10 === 1 && mod100 !== 11) return `${count} предупреждение`;
  if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return `${count} предупреждения`;
  return `${count} предупреждений`;
}

function isInternalLink(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === 'http:' || url.protocol === 'https:';
  } catch {
    return false;
  }
}

export function TaskCard({ run, templateTitle, stageTitle, now, onCancel }: TaskCardProps) {
  const navigate = useNavigate();
  const title = taskCardTitle(run.task, templateTitle);
  const started = new Date(run.started_at).toLocaleString('ru-RU', {
    day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit',
  });
  const onStageSince = run.stage_entered_at ? new Date(run.stage_entered_at).getTime() : null;
  const onStage = onStageSince !== null && now !== undefined ? formatStageDuration(now - onStageSince) : null;
  const stageLabel = run.current_stage ? (stageTitle?.(run.current_stage) ?? run.current_stage) : '';
  const executor = executorLabel(run);
  const links = run.external_links ?? [];

  return (
    <article
      className={styles.card}
      aria-label={`Задача ${title}`}
      onClick={() => navigate(`/tasks/${encodeURIComponent(run.run_id)}`)}
      onKeyDown={(event) => {
        if (event.target === event.currentTarget && (event.key === 'Enter' || event.key === ' ')) {
          event.preventDefault();
          navigate(`/tasks/${encodeURIComponent(run.run_id)}`);
        }
      }}
      role="link"
      tabIndex={0}
    >
      <span className={styles.chip} data-tone={statusTone[run.status] ?? 'gray'}>
        {statusLabels[run.status] ?? run.status}
      </span>
      <h3 className={styles.title}>{title}</h3>
      {(stageLabel || executor) && (
        <dl className={styles.meta}>
          {stageLabel && <div><dt>Этап</dt><dd>{stageLabel}</dd></div>}
          {executor && <div><dt>Исполнитель</dt><dd>{executor}</dd></div>}
        </dl>
      )}
      <div className={styles.service} onClick={(event) => event.stopPropagation()}>
        <dl className={styles.details}>
          <div><dt>Идентификатор</dt><dd><code className={styles.ident}>{run.feature}</code></dd></div>
          <div><dt>ID</dt><dd><code title={run.run_id}>{shortenHash(run.run_id)}</code></dd></div>
          {run.current_function && <div><dt>Функция</dt><dd>{run.current_function}</dd></div>}
          <div><dt>На этапе</dt><dd>{onStage ?? '—'}</dd></div>
          <div><dt>Режим</dt><dd>{modeLabel(run)}</dd></div>
          <div><dt>Предупреждения</dt><dd>{warningsLabel(run.warnings)}</dd></div>
          <div><dt>Начата</dt><dd>{started}</dd></div>
        </dl>
        {run.status === 'queued' && onCancel ? (
          <button type="button" className={styles.cancel} onClick={(event) => {
            event.stopPropagation(); onCancel();
          }}>Отменить</button>
        ) : null}
      </div>
      {links.length > 0 && (
        <ul className={styles.links} aria-label="Внешние ссылки" onClick={(event) => event.stopPropagation()}>
          {links.map((link) => (
            <li key={link}>
              {isInternalLink(link)
                ? <a href={link} target="_blank" rel="noreferrer">{link}</a>
                : <code>{link}</code>}
            </li>
          ))}
        </ul>
      )}
    </article>
  );
}
