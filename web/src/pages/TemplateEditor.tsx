import { useCallback, useEffect, useState } from 'react';
import { getActivePrincipal, getProjectTemplate, publishProjectTemplate, validateProjectTemplate } from '../api';
import type { ProjectTemplateResponse, TemplateValidation } from '../types';
import { usePageTitle } from '../hooks/usePageTitle';
import styles from './TemplateEditor.module.css';

function shortVersion(version: string): string {
  return `${version.slice(0, 12)}…${version.slice(-8)}`;
}

export function TemplateEditor() {
  usePageTitle('Шаблон проекта');
  const principal = getActivePrincipal();
  const canEdit = !principal || principal.roles.includes('product_owner') || principal.roles.includes('architect');
  const [project, setProject] = useState<ProjectTemplateResponse | null>(null);
  const [draft, setDraft] = useState('');
  const [validation, setValidation] = useState<TemplateValidation | null>(null);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState('');
  const [error, setError] = useState('');

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const fresh = await getProjectTemplate();
      setProject(fresh);
      setDraft(fresh.template.yaml ?? '');
      setValidation(fresh.template.valid ? fresh.template : null);
      setError('');
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Не удалось загрузить шаблон проекта.');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void refresh(); }, [refresh]);

  const changed = Boolean(project && draft !== project.template.yaml);
  const validationMatchesDraft = Boolean(validation?.valid && validation.version &&
    validation.version === project?.template.version && !changed);

  const validate = async () => {
    setSaving(true);
    setMessage('Проверяем YAML и граф…');
    setError('');
    try {
      const result = await validateProjectTemplate(draft);
      setValidation(result);
      setMessage(result.valid ? 'Шаблон прошёл проверку.' : 'Шаблон содержит ошибки. Исправьте их перед публикацией.');
      return result;
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Не удалось проверить шаблон.');
      setMessage('');
      return null;
    } finally {
      setSaving(false);
    }
  };

  const publish = async () => {
    if (!project || !canEdit) return;
    setSaving(true);
    setError('');
    setMessage('Проверяем и публикуем версию…');
    try {
      const result = await validateProjectTemplate(draft);
      setValidation(result);
      if (!result.valid) {
        setMessage('Шаблон содержит ошибки. Исправьте их перед публикацией.');
        return;
      }
      await publishProjectTemplate(draft, project.template.version ?? '');
      setMessage('Новая версия опубликована. Новые задачи закрепят её автоматически.');
      await refresh();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Не удалось опубликовать шаблон.');
      setMessage('');
    } finally {
      setSaving(false);
    }
  };

  if (loading) return <main className={styles.state}>Загружаем шаблон проекта…</main>;
  if (error && !project) return <main className={styles.error} role="alert">{error}</main>;
  if (!project) return <main className={styles.error} role="alert">Шаблон проекта недоступен.</main>;

  const active = project.template;
  const displayed = changed ? validation : active;
  const stages = displayed?.stages ?? [];
  const returnRoutes = displayed?.returns ?? [];

  return (
    <main className={styles.container}>
      <header className={styles.header}>
        <div>
          <h1>Шаблон проекта</h1>
          <p>У проекта один выбранный шаблон: его граф используется для новых задач.</p>
        </div>
        <div className={styles.version}>
          <span>Опубликованная версия</span>
          <code title={active.version}>{active.version ? shortVersion(active.version) : 'не определена'}</code>
        </div>
      </header>

      <p className={styles.pinNotice}>Уже созданные задачи сохраняют свою закреплённую версию при продолжении и восстановлении. Публикация влияет на новые задачи.</p>

      <section className={styles.panel} aria-labelledby="template-flow-heading">
        <div className={styles.panelHeader}>
          <div>
            <h2 id="template-flow-heading">{displayed?.title ?? active.title ?? 'Маршрут'}</h2>
            <span>{displayed?.template ?? active.template}</span>
          </div>
          {changed && <span className={styles.draftBadge}>Черновик</span>}
        </div>
        {stages.length > 0 ? (
          <ol className={styles.flow} aria-label="Этапы шаблона по порядку">
            {stages.map((stage, index) => (
              <li key={stage.id}>
                <article className={styles.stage}>
                  <span className={styles.stageNumber}>{index + 1}</span>
                  <div className={styles.stageText}>
                    <strong>{stage.title}</strong>
                    <span><code>{stage.id}</code> · функция: {stage.function} · результат: {stage.result}</span>
                    <span>{stage.executor === 'agent' ? `Агент: ${stage.agent ?? 'не назначен'}` : 'Исполняет участник'}
                      {stage.max_visits ? ` · лимит возвратов: ${stage.max_visits}` : ''}</span>
                  </div>
                </article>
                {index < stages.length - 1 && <span className={styles.forward} aria-hidden="true">↓</span>}
              </li>
            ))}
          </ol>
        ) : (
          <p className={styles.emptyGraph}>Сначала исправьте ошибки YAML, чтобы увидеть схему.</p>
        )}
        <section className={styles.returns} aria-label="Маршруты возврата">
          <h3>Возвраты по результатам ревью</h3>
          {returnRoutes.length === 0 ? <p>Возвратные маршруты не заданы.</p> : returnRoutes.map((route) => (
            <div className={styles.returnRoute} key={`${route.from}-${route.to}`}>
              <span aria-hidden="true">↶</span>
              <code>{route.from}</code><span>возвращает в</span><code>{route.to}</code>
              {route.max_visits ? <small>до {route.max_visits} посещений</small> : null}
            </div>
          ))}
        </section>
      </section>

      <section className={styles.panel} aria-labelledby="template-yaml-heading">
        <div className={styles.panelHeader}>
          <div>
            <h2 id="template-yaml-heading">YAML шаблона</h2>
            <p>Изменения применяются только после успешной проверки и публикации.</p>
          </div>
          {changed && <span className={styles.unsaved}>Есть неопубликованные изменения</span>}
        </div>
        <label className={styles.editorLabel} htmlFor="template-yaml">Конфигурация выбранного шаблона</label>
        <textarea id="template-yaml" className={styles.editor} value={draft} disabled={!canEdit || saving}
          spellCheck={false} onChange={(event) => { setDraft(event.target.value); setValidation(null); setMessage(''); }} />
        {!canEdit && <p>Редактировать шаблон могут Product Owner и Architect.</p>}
        {message && <p className={styles.status} role="status">{message}</p>}
        {validation?.diagnostic && <pre className={styles.diagnostic} role="alert">{validation.diagnostic}</pre>}
        {error && <p className={styles.error} role="alert">{error}</p>}
        {canEdit && <div className={styles.actions}>
          <button type="button" onClick={() => void validate()} disabled={saving}>{saving ? 'Подождите…' : 'Проверить YAML'}</button>
          <button type="button" onClick={() => void publish()} disabled={saving || !changed || !active.version}>
            {saving ? 'Публикуем…' : 'Проверить и опубликовать версию'}
          </button>
          {changed && <button type="button" className={styles.secondary} onClick={() => { setDraft(active.yaml ?? ''); setValidation(null); setMessage(''); setError(''); }}>Отменить изменения</button>}
        </div>}
        {validationMatchesDraft && <p className={styles.status}>Текущая опубликованная конфигурация валидна.</p>}
      </section>

      <section className={styles.panel} aria-labelledby="template-versions-heading">
        <h2 id="template-versions-heading">Опубликованные версии</h2>
        <ul className={styles.versionList}>
          {project.versions.map((version) => (
            <li key={version.id}>
              <code title={version.id}>{shortVersion(version.id)}</code>
              {version.id === active.version && <strong>текущая</strong>}
              {version.created_at && <time dateTime={version.created_at}>{new Date(version.created_at).toLocaleString('ru-RU')}</time>}
            </li>
          ))}
        </ul>
      </section>
    </main>
  );
}
