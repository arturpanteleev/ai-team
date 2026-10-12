import type { TaskRun } from '../types';
import { formatStageDuration } from '../duration';
import { useNavigate } from '../router';
import { CopyableHash } from './CopyableHash';
import { StatusBadge } from './StatusBadge';
import styles from './TaskCard.module.css';

interface TaskCardProps {
  run: TaskRun;
  /** Заголовок этапа из шаблона; id этапа, если шаблон недоступен. */
  stageTitle?: (stageId: string) => string;
  /** Текущий момент (мс) для «сколько на этапе»; обновляется тикером страницы. */
  now?: number;
  onCancel?: () => void;
}

function executorLabel(run: TaskRun): string {
  if (run.executor === 'agent') return run.executor_agent ? `Агент: ${run.executor_agent}` : 'Агент';
  if (run.executor === 'human') return 'Участник';
  return '—';
}

function modeLabel(run: TaskRun): string {
  if (run.mode === 'auto') return 'Авто';
  if (run.mode === 'manual') return 'Вручную';
  return '—';
}

function isInternalLink(value: string): boolean {
  try {
    const url = new URL(value);
    return url.protocol === 'http:' || url.protocol === 'https:';
  } catch {
    return false;
  }
}

export function TaskCard({ run, stageTitle, now, onCancel }: TaskCardProps) {
  const navigate = useNavigate();
  const started = new Date(run.started_at).toLocaleString('ru-RU', {
    day: '2-digit', month: '2-digit', hour: '2-digit', minute: '2-digit',
  });
  const onStageSince = run.stage_entered_at ? new Date(run.stage_entered_at).getTime() : null;
  const onStage = onStageSince !== null && now !== undefined ? formatStageDuration(now - onStageSince) : null;
  const stageLabel = run.current_stage
    ? (stageTitle?.(run.current_stage) ?? run.current_stage)
    : '—';

  return (
    <article
      className={styles.card}
      aria-label={`Задача ${run.feature}`}
      onClick={() => navigate(`/pipelines/${run.id}`)}
      onKeyDown={(event) => {
        if (event.target === event.currentTarget && (event.key === 'Enter' || event.key === ' ')) {
          event.preventDefault();
          navigate(`/pipelines/${run.id}`);
        }
      }}
      role="link"
      tabIndex={0}
    >
      <div className={styles.header}>
        <span className={styles.feature}>{run.feature}</span>
        <StatusBadge status={run.status} />
      </div>
      {run.task && <p className={styles.task}>{run.task}</p>}
      <dl className={styles.meta}>
        <div><dt>Этап</dt><dd>{stageLabel}{run.current_function ? ` · ${run.current_function}` : ''}</dd></div>
        <div><dt>Исполнитель</dt><dd>{executorLabel(run)}</dd></div>
        <div><dt>На этапе</dt><dd>{onStage ?? '—'}</dd></div>
        <div><dt>Режим</dt><dd>{modeLabel(run)}</dd></div>
      </dl>
      <div className={styles.footer} onClick={(event) => event.stopPropagation()}>
        <span className={styles.warningBadge} data-has-warnings={run.warnings > 0}
          title={run.warnings > 0 ? `Предупреждений: ${run.warnings}` : 'Предупреждений нет'}>
          <span aria-hidden="true">⚠</span> {run.warnings}
        </span>
        <CopyableHash value={run.run_id} label="ID задачи" />
        <span className={styles.started}>{started}</span>
        {run.status === 'queued' && onCancel ? (
          <button type="button" onClick={(event) => {
            event.stopPropagation(); onCancel();
          }}>Отменить</button>
        ) : null}
      </div>
      {(run.external_links ?? []).length > 0 && (
        <ul className={styles.links} aria-label="Внешние ссылки" onClick={(event) => event.stopPropagation()}>
          {(run.external_links ?? []).map((link) => (
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
