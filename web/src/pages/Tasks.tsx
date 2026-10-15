import { useCallback, useEffect, useMemo, useState } from 'react';
import type { FormEvent } from 'react';
import { cancelRun, getTaskRuns, getTemplatesList } from '../api';
import { Link, useSearchParams } from '../router';
import { useWebSocket } from '../hooks/useWebSocket';
import { usePageTitle } from '../hooks/usePageTitle';
import { projectLabel, useProjects } from '../hooks/useProjects';
import { TaskCard } from '../components/TaskCard';
import type { TaskRun, TaskRunFilters, TemplateValidation } from '../types';
import { statusLabels } from '../statusLabels';
import styles from './Tasks.module.css';

type ViewMode = 'board' | 'list';

const PAGE_SIZE = 50;
// Длительность на этапе показывается с точностью до минуты — тиканья раз в
// 30 секунд достаточно. Данные приходят по WebSocket; периодический рефетч
// лишь страхует от пропущенных событий, поэтому он заметно реже.
const STAGE_CLOCK_INTERVAL_MS = 30_000;
const RECOVERY_REFETCH_INTERVAL_MS = 120_000;

const statusOptions = [
  'queued',
  'running',
  'waiting_for_approval',
  'blocked',
  'completed',
  'completed_with_warnings',
  'failed',
  'stopped',
  'canceled',
  'interrupted',
] as const;

// Доска показывает только активные задачи: у терминальных нет текущего этапа,
// их история остаётся в списке.
const activeStatuses = new Set(['queued', 'running', 'waiting_for_approval', 'blocked']);

const modeOptions = [
  { value: 'manual', label: 'Вручную' },
  { value: 'auto', label: 'Авто' },
];

// Служебные колонки доски: очередь (реально стоящие в очереди прогоны) и
// прогоны, у которых этап не известен из данных (заблокированы/ждут решения).
const QUEUE_COLUMN_ID = '__queued__';
const WAITING_COLUMN_ID = '__waiting__';

interface BoardColumn {
  id: string;
  title: string;
  kind: 'queue' | 'stage' | 'waiting';
}

function boardColumnId(run: TaskRun): string {
  if (run.status === 'queued') return QUEUE_COLUMN_ID;
  return run.current_stage || WAITING_COLUMN_ID;
}

export function Tasks() {
  usePageTitle('Задачи');
  const [view, setView] = useState<ViewMode>('board');
  // Проект выбирается в адресе (?project=ID): ссылку можно сохранить и передать.
  const [searchParams, setSearchParams] = useSearchParams();
  const projectParam = searchParams.get('project') ?? '';
  const { projects } = useProjects();
  const selectedProject = projects.find((project) => String(project.id) === projectParam);
  const [runs, setRuns] = useState<TaskRun[]>([]);
  const [total, setTotal] = useState(0);
  const [offset, setOffset] = useState(0);
  const [templates, setTemplates] = useState<TemplateValidation | null>(null);
  const [loading, setLoading] = useState(true);
  const [status, setStatus] = useState('');
  const [stage, setStage] = useState('');
  const [fn, setFn] = useState('');
  const [mode, setMode] = useState('');
  const [commandStatus, setCommandStatus] = useState('');
  const [now, setNow] = useState(() => Date.now());

  const fetchRuns = useCallback(async (activeOffset: number, filters: TaskRunFilters) => {
    try {
      const data = await getTaskRuns({ ...filters, limit: PAGE_SIZE, offset: activeOffset });
      setRuns(data.runs ?? []);
      setTotal(data.total ?? 0);
    } catch (err) {
      console.error('Failed to fetch tasks:', err);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    getTemplatesList()
      .then((response) => setTemplates(response.templates?.[0] ?? null))
      .catch(() => setTemplates(null));
  }, []);

  const filters = useMemo<TaskRunFilters>(
    () => ({ status, stage, function: fn, mode, project_id: projectParam }),
    [status, stage, fn, mode, projectParam],
  );

  useEffect(() => {
    void fetchRuns(offset, filters);
  }, [fetchRuns, filters, offset]);

  useWebSocket({
    onEvent: () => {
      void fetchRuns(offset, filters);
    },
  });

  // Часы для «сколько на этапе»: только перерисовка, без запросов к API.
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), STAGE_CLOCK_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, []);

  // Редкий recovery fallback; live source — durable WebSocket event stream.
  useEffect(() => {
    const timer = window.setInterval(() => void fetchRuns(offset, filters), RECOVERY_REFETCH_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [fetchRuns, filters, offset]);

  const stages = useMemo(() => templates?.stages ?? [], [templates]);
  const stageTitle = useCallback(
    (stageId: string) => stages.find((value) => value.id === stageId)?.title ?? stageId,
    [stages],
  );
  const functions = useMemo(() => {
    const unique = new Map<string, string>();
    for (const value of stages) {
      if (value.function) unique.set(value.function, value.function);
    }
    for (const run of runs) {
      if (run.current_function) unique.set(run.current_function, run.current_function);
    }
    return [...unique.values()];
  }, [stages, runs]);

  const boardRuns = runs.filter((run) => activeStatuses.has(run.status));
  const columns = useMemo<BoardColumn[]>(() => {
    const known = new Set(stages.map((value) => value.id));
    const extra = new Set<string>();
    for (const run of boardRuns) {
      if (run.status !== 'queued' && run.current_stage && !known.has(run.current_stage)) extra.add(run.current_stage);
    }
    return [
      { id: QUEUE_COLUMN_ID, title: 'В очереди', kind: 'queue' },
      ...stages.map((value) => ({ id: value.id, title: value.title, kind: 'stage' as const })),
      ...[...extra].map((id) => ({ id, title: id, kind: 'stage' as const })),
      { id: WAITING_COLUMN_ID, title: 'Ожидает решения', kind: 'waiting' },
    ];
  }, [stages, boardRuns]);

  const submitCancel = async (run: TaskRun) => {
    setCommandStatus('');
    try {
      await cancelRun(run.run_id);
      await fetchRuns(offset, filters);
    } catch (err) {
      setCommandStatus(err instanceof Error ? err.message : 'Не удалось отменить задачу');
    }
  };

  const changeFilter = (setter: (value: string) => void) => (event: FormEvent<HTMLSelectElement>) => {
    setter(event.currentTarget.value);
    setOffset(0);
  };

  const changeProject = (event: FormEvent<HTMLSelectElement>) => {
    setSearchParams(event.currentTarget.value ? { project: event.currentTarget.value } : {});
    setOffset(0);
  };

  // Новый проект без задач: вместо пустой доски — объяснение и первый шаг.
  const projectIsEmpty = !loading && Boolean(projectParam) && total === 0 && !status && !stage && !fn && !mode;

  return (
    <div className={styles.container}>
      <div className={styles.header}>
        <h1 className={styles.title}>Задачи</h1>
        <div className={styles.headerActions}>
          <div className={styles.viewToggle} role="group" aria-label="Вид">
            <button type="button" aria-pressed={view === 'board'} className={view === 'board' ? styles.active : ''}
              onClick={() => setView('board')}>Доска</button>
            <button type="button" aria-pressed={view === 'list'} className={view === 'list' ? styles.active : ''}
              onClick={() => setView('list')}>Список</button>
          </div>
          <Link className={styles.newTask} to={projectParam ? `/tasks/new?project=${encodeURIComponent(projectParam)}` : '/tasks/new'}>Новая задача</Link>
        </div>
      </div>

      <div className={styles.filters} aria-label="Фильтры задач">
        <label>
          Проект
          <select value={projectParam} onChange={changeProject}>
            <option value="">Все</option>
            {projects.map((project) => (
              <option key={project.id} value={project.id}>{projectLabel(project)}</option>
            ))}
          </select>
        </label>
        <label>
          Состояние
          <select value={status} onChange={changeFilter(setStatus)}>
            <option value="">Все</option>
            {statusOptions.map((value) => (
              <option key={value} value={value}>{statusLabels[value] ?? value}</option>
            ))}
          </select>
        </label>
        <label>
          Этап
          <select value={stage} onChange={changeFilter(setStage)}>
            <option value="">Все</option>
            {stages.map((value) => (
              <option key={value.id} value={value.id}>{value.title}</option>
            ))}
          </select>
        </label>
        <label>
          Функция
          <select value={fn} onChange={changeFilter(setFn)}>
            <option value="">Все</option>
            {functions.map((value) => (
              <option key={value} value={value}>{value}</option>
            ))}
          </select>
        </label>
        <label>
          Режим
          <select value={mode} onChange={changeFilter(setMode)}>
            <option value="">Все</option>
            {modeOptions.map((option) => (
              <option key={option.value} value={option.value}>{option.label}</option>
            ))}
          </select>
        </label>
      </div>

      {commandStatus && <p className={styles.commandStatus} role="alert">{commandStatus}</p>}

      {loading ? (
        <div className={styles.empty}>Загружаем задачи…</div>
      ) : projectIsEmpty ? (
        <section className={styles.emptyProject} aria-label="Пустой проект">
          <h2>{selectedProject ? `В проекте «${selectedProject.name}» пока нет задач` : 'В этом проекте пока нет задач'}</h2>
          {selectedProject?.description && <p>{selectedProject.description}</p>}
          {selectedProject?.archived
            ? <p>Проект в архиве: новые задачи он не принимает.</p>
            : <>
              <p>Проект — папка для задач. Создайте первую задачу, и она появится здесь на доске и в списке.</p>
              <Link className={styles.newTask} to={`/tasks/new?project=${encodeURIComponent(projectParam)}`}>Создать первую задачу</Link>
            </>}
        </section>
      ) : view === 'board' ? (
        <section className={styles.board} aria-label="Доска по этапам">
          {columns.map((column) => {
            const columnRuns = boardRuns.filter((run) => boardColumnId(run) === column.id);
            return (
              <section key={column.id} className={styles.column} data-kind={column.kind}
                aria-label={`Этап: ${column.title}`}>
                <header className={styles.columnHeader}>
                  <strong>{column.title}</strong>
                  <span className={styles.columnCount}>{columnRuns.length}</span>
                </header>
                <div className={styles.columnBody}>
                  {columnRuns.length === 0
                    ? <p className={styles.columnEmpty}>Нет задач</p>
                    : columnRuns.map((run) => (
                      <TaskCard key={run.run_id} run={run} stageTitle={stageTitle} templateTitle={templates?.title}
                        now={now} onCancel={() => void submitCancel(run)} />
                    ))}
                </div>
              </section>
            );
          })}
        </section>
      ) : runs.length === 0 ? (
        <div className={styles.empty}>Задач по выбранным фильтрам нет.</div>
      ) : (
        <div className={styles.list} aria-label="Список задач">
          {runs.map((run) => (
            <TaskCard key={run.run_id} run={run} stageTitle={stageTitle} templateTitle={templates?.title}
              now={now} onCancel={() => void submitCancel(run)} />
          ))}
        </div>
      )}

      {!loading && total > PAGE_SIZE && (
        <nav className={styles.pagination} aria-label="Постраничная навигация">
          <button type="button" disabled={offset === 0}
            onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}>Назад</button>
          <span>{offset + 1}–{Math.min(offset + PAGE_SIZE, total)} из {total}</span>
          <button type="button" disabled={offset + PAGE_SIZE >= total}
            onClick={() => setOffset(offset + PAGE_SIZE)}>Вперёд</button>
        </nav>
      )}
    </div>
  );
}
