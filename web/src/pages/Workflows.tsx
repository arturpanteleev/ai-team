import { useEffect, useId, useState } from 'react';
import { Link, useParams } from '../router';
import { getActivePrincipal, getWorkflow, getWorkflows, updateWorkflowProcess } from '../api';
import { usePageTitle } from '../hooks/usePageTitle';
import { mayPerform } from '../permissions';
import type { WorkflowDetail, WorkflowGateGroups, WorkflowsResponse } from '../types/workflow';
import styles from './Workflows.module.css';

function errorMessage(reason: unknown, fallback: string): string {
  return reason instanceof Error ? reason.message : fallback;
}

export function Workflows() {
  usePageTitle('Workflow');
  const [response, setResponse] = useState<WorkflowsResponse | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    getWorkflows().then((next) => {
      if (active) {
        setResponse(next);
        setError('');
      }
    }).catch((reason: unknown) => {
      if (active) setError(errorMessage(reason, 'Не удалось загрузить каталог workflow.'));
    });
    return () => { active = false; };
  }, []);

  return (
    <main className={styles.container}>
      <header className={styles.header}>
        <h1>Workflow</h1>
        <p className={styles.lede}>
          Workflow — класс: именованная версия набора графов с эффективными гейтами входа и выхода.
          Конкретная задача — прогон одного из этих графов; её граф открывается со страницы задачи.
        </p>
      </header>
      {error && <p className={styles.error} role="alert">{error}</p>}
      {!response && !error ? <p className={styles.muted}>Загружаем workflow…</p> : (
        response && (response.workflows.length === 0
          ? <p className={styles.muted}>Workflow в конфигурации не объявлены.</p>
          : <ul className={styles.cards}>
            {response.workflows.map((workflow) => (
              <li key={workflow.id}>
                <Link to={`/workflows/${encodeURIComponent(workflow.id)}`} className={styles.card}>
                  <span className={styles.cardTop}>
                    <strong>{workflow.title || workflow.id}</strong>
                    {workflow.active && <span className={styles.badge}>Активный</span>}
                  </span>
                  {workflow.description && <span className={styles.cardDescription}>{workflow.description}</span>}
                  <span className={styles.cardMeta}>
                    <code>{workflow.id}</code> · версия {workflow.version} · графов: {workflow.graph_count}
                    {workflow.id === response.active_workflow_id ? ' · используется по умолчанию' : ''}
                  </span>
                </Link>
              </li>
            ))}
          </ul>)
      )}
    </main>
  );
}

function GateChains({ title, groups }: { title: string; groups: WorkflowGateGroups }) {
  const headingID = useId();
  return (
    <section className={styles.section} aria-labelledby={headingID}>
      <h2 id={headingID}>{title}</h2>
      {groups.length === 0
        ? <p className={styles.muted}>Гейтов нет.</p>
        : <ol className={styles.groups}>
          {groups.map((group, groupIndex) => (
            <li key={groupIndex} className={styles.group}>
              <span className={styles.groupLabel}>Группа {groupIndex + 1}</span>
              <ul className={styles.gates}>
                {group.map((gate, gateIndex) => (
                  <li key={`${gate.gate}:${gateIndex}`}>
                    <code>{gate.gate}@{gate.version}</code>
                    {gate.name && <> · {gate.name}</>}
                    {gate.mode === 'advisory' && <span className={styles.advisory}>рекомендация</span>}
                  </li>
                ))}
              </ul>
            </li>
          ))}
        </ol>}
    </section>
  );
}

export function WorkflowDetailPage() {
  const { workflowID = '' } = useParams<{ workflowID: string }>();
  const [detail, setDetail] = useState<WorkflowDetail | null>(null);
  const [error, setError] = useState('');
  usePageTitle(detail?.title ?? 'Workflow');

  useEffect(() => {
    let active = true;
    setDetail(null);
    setError('');
    getWorkflow(workflowID).then((next) => {
      if (active) {
        setDetail(next);
        setError('');
      }
    }).catch((reason: unknown) => {
      if (active) setError(errorMessage(reason, 'Не удалось загрузить workflow.'));
    });
    return () => { active = false; };
  }, [workflowID]);

  if (error) return <main className={styles.container} role="alert">{error}</main>;
  if (!detail) return <main className={styles.container}><p className={styles.muted}>Загружаем workflow…</p></main>;

  return (
    <main className={styles.container}>
      <header className={styles.header}>
        <Link to="/workflows" className={styles.backLink}>← К списку workflow</Link>
        <h1>{detail.title || detail.id}</h1>
        <p className={styles.lede}>{detail.description || 'Описание в конфигурации не задано.'}</p>
        <dl className={styles.meta}>
          <div><dt>Идентификатор</dt><dd><code>{detail.id}</code></dd></div>
          <div><dt>Версия</dt><dd>{detail.version}</dd></div>
          <div><dt>Статус</dt><dd>{detail.active ? 'Активный' : 'Неактивный'}</dd></div>
        </dl>
      </header>

      <WorkflowProcessEditor detail={detail} onSaved={() => { void getWorkflow(workflowID).then(setDetail); }} />

      <section className={styles.section} aria-labelledby="workflow-graphs-heading">
        <h2 id="workflow-graphs-heading">Графы</h2>
        {detail.graphs.length === 0
          ? <p className={styles.muted}>У workflow нет объявленных графов.</p>
          : <ul className={styles.cards}>
            {detail.graphs.map((graph) => (
              <li key={graph.graph_id || graph.entry}>
                <Link to={`/graphs/${encodeURIComponent(graph.graph_id || graph.entry)}`} className={styles.card}>
                  <span className={styles.cardTop}><strong>{graph.title || graph.graph_id || 'Граф'}</strong></span>
                  <span className={styles.cardMeta}>
                    <code>{graph.graph_id || '—'}</code> · вход: <code>{graph.entry}</code> · узлов: {graph.nodes.length} · рёбер: {graph.edges.length}
                  </span>
                </Link>
              </li>
            ))}
          </ul>}
      </section>

      <div className={styles.gateGrid}>
        <GateChains title="Гейты входа (эффективные)" groups={detail.entry_gate_groups} />
        <GateChains title="Гейты выхода (эффективные)" groups={detail.exit_gate_groups} />
      </div>

      <section className={styles.section} aria-labelledby="workflow-tasks-heading">
        <h2 id="workflow-tasks-heading">Задачи workflow</h2>
        {detail.tasks.length === 0
          ? <p className={styles.muted}>Задач, закреплённых за этим workflow, пока нет.</p>
          : <ul className={styles.tasks}>
            {detail.tasks.map((task) => (
              <li key={task.run_id}>
                <Link to={`/tasks/${encodeURIComponent(task.run_id)}`}>{task.feature || task.run_id}</Link>
                {task.status && <span className={styles.taskStatus}>{task.status}</span>}
                {task.current_stage && <span className={styles.taskStage}>этап: <code>{task.current_stage}</code></span>}
              </li>
            ))}
          </ul>}
      </section>
    </main>
  );
}

// WF-06: процессные поля доступны только тем, у кого есть template:edit.
// Команды, гейты, выбор workflow и инфраструктурные настройки здесь не
// редактируются — они задаются файловым конфигом; сервер вернёт
// trusted_config_only на любую попытку изменить их через API.
function WorkflowProcessEditor({ detail, onSaved }: { detail: WorkflowDetail; onSaved: () => void }) {
  const headingID = useId();
  const titleID = useId();
  const descriptionID = useId();
  const canEdit = mayPerform(getActivePrincipal(), 'template:edit');
  const [title, setTitle] = useState(detail.title);
  const [description, setDescription] = useState(detail.description ?? '');
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    setTitle(detail.title);
    setDescription(detail.description ?? '');
    setSaveError('');
    setSaved(false);
  }, [detail.id, detail.title, detail.description]);

  const changed = title !== detail.title || description !== (detail.description ?? '');

  const save = async () => {
    if (!title.trim()) {
      setSaveError('Название workflow обязательно.');
      setSaved(false);
      return;
    }
    setSaving(true);
    setSaveError('');
    setSaved(false);
    try {
      await updateWorkflowProcess(detail.id, { title: title.trim(), description });
      setSaved(true);
      onSaved();
    } catch (reason: unknown) {
      setSaveError(`Правка не сохранена: ${errorMessage(reason, 'сервер отклонил изменение.')}`);
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className={styles.section} aria-labelledby={headingID}>
      <h2 id={headingID}>Настройки workflow</h2>
      <p className={styles.editorHint}>
        Название и описание — процессные поля: они сохраняются в файловом конфиге и попадают в аудит.
        Команды, гейты, графы, выбор активного workflow и инфраструктура правятся только в конфиге репозитория.
      </p>
      <div className={styles.editorFields}>
        <label className={styles.field} htmlFor={titleID}>
          <span>Название</span>
          <input id={titleID} className={styles.input} value={title} onChange={(event) => setTitle(event.target.value)} disabled={!canEdit || saving} />
        </label>
        <label className={styles.field} htmlFor={descriptionID}>
          <span>Описание</span>
          <textarea id={descriptionID} className={styles.textarea} rows={3} value={description}
            onChange={(event) => setDescription(event.target.value)} disabled={!canEdit || saving} />
        </label>
      </div>
      {!canEdit && <p className={styles.muted}>Право template:edit нужно для правки процессных полей.</p>}
      {saveError && <p className={styles.error} role="alert">{saveError}</p>}
      {saved && !saveError && <p className={styles.saved} role="status">Изменение сохранено и записано в аудит.</p>}
      {canEdit && <div className={styles.editorActions}>
        <button type="button" onClick={() => void save()} disabled={saving || !changed}>
          {saving ? 'Сохраняем…' : 'Сохранить'}
        </button>
        {changed && <button type="button" className={styles.secondary}
          onClick={() => { setTitle(detail.title); setDescription(detail.description ?? ''); setSaveError(''); setSaved(false); }} disabled={saving}>
          Отменить
        </button>}
      </div>}
    </section>
  );
}
