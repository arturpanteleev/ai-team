import { useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import { createTask, getActivePrincipal, getTemplatesList, isAuthenticationRequired } from '../api';
import { Link } from '../router';
import { usePageTitle } from '../hooks/usePageTitle';
import type { TemplateValidation } from '../types';
import styles from './NewTask.module.css';

const modeOptions = [
  { value: 'manual', label: 'Вручную', hint: 'Человек подтверждает этапы и решения.' },
  { value: 'auto', label: 'Авто', hint: 'Этапы выполняются подтверждённым агентным шаблоном без ручных согласований.' },
];

// Один черновик — один идемпотентный ключ: повтор после сетевого сбоя
// не создаёт вторую задачу, а новая форма получает новый ключ.
function newDraftKey(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID();
  return `draft-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

export function NewTask() {
  usePageTitle('Новая задача');
  const principal = getActivePrincipal();
  // Без principal форма доступна только в локальном режиме. В облачном режиме
  // null значит «нет сессии» — такому пользователю форму не открываем.
  const signedOutInCloud = !principal && isAuthenticationRequired();
  const canCreate = principal ? principal.roles.includes('product_owner') : !signedOutInCloud;
  const [templates, setTemplates] = useState<TemplateValidation[]>([]);
  const [template, setTemplate] = useState('');
  const [name, setName] = useState('');
  const [mode, setMode] = useState('manual');
  const [idea, setIdea] = useState('');
  const [links, setLinks] = useState('');
  const [status, setStatus] = useState('');
  const [createdRunID, setCreatedRunID] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);
  // Идемпотентный ключ живёт до первой правки после неудачной отправки:
  // повтор неизменённого черновика replays тот же ответ сервера, а изменённая
  // форма получает новый ключ и не провоцирует idempotency_conflict.
  const [draftKey, setDraftKey] = useState(newDraftKey);
  const [failed, setFailed] = useState(false);
  const touchDraft = () => {
    if (failed) {
      setFailed(false);
      setDraftKey(newDraftKey());
    }
  };

  useEffect(() => {
    getTemplatesList()
      .then((response) => {
        const valid = (response.templates ?? []).filter((value) => value.valid);
        setTemplates(valid);
        const active = valid.find((value) => value.template === response.active_template) ?? valid[0];
        if (active?.template) setTemplate(active.template);
      })
      .catch(() => setTemplates([]));
  }, []);

  if (signedOutInCloud) {
    return <main className={styles.state}>Войдите, чтобы создавать задачи.</main>;
  }
  if (!canCreate) {
    return <main className={styles.state}>Создавать задачи может Product Owner.</main>;
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSubmitting(true);
    setStatus('Создаём задачу…');
    setError('');
    try {
      const externalLinks = links.split('\n').map((line) => line.trim()).filter(Boolean);
      const result = await createTask({
        feature: name.trim(),
        task: idea.trim(),
        title: name.trim(),
        idea: idea.trim(),
        template: template || undefined,
        mode: mode === 'auto' ? 'auto' : 'manual',
        external_links: externalLinks.length > 0 ? externalLinks : undefined,
      }, `create-task-${draftKey}`);
      setCreatedRunID(result.run_id);
      setStatus(`Задача создана: ${result.run_id}`);
      setName('');
      setIdea('');
      setLinks('');
      setFailed(false);
      setDraftKey(newDraftKey());
    } catch (reason) {
      setStatus('');
      setFailed(true);
      setError(reason instanceof Error ? reason.message : 'Не удалось создать задачу.');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <main className={styles.container}>
      <header className={styles.header}>
        <h1>Новая задача</h1>
        <p>Задача стартует по шаблону проекта: текст идеи заполняет этап intent, внешние ссылки сохраняются вместе с задачей.</p>
      </header>

      <form className={styles.form} onSubmit={submit}>
        <label htmlFor="task-name">Название</label>
        <input id="task-name" value={name} onChange={(event) => { touchDraft(); setName(event.target.value); }}
          placeholder="например, онбординг для новых клиентов" required />

        <label htmlFor="task-template">Шаблон</label>
        <select id="task-template" value={template} onChange={(event) => { touchDraft(); setTemplate(event.target.value); }}
          required={templates.length > 0} disabled={templates.length === 0}>
          {templates.length === 0 && <option value="">Шаблоны недоступны</option>}
          {templates.map((value) => (
            <option key={value.template} value={value.template}>
              {value.title ?? value.template}{value.version ? ` · ${value.version.slice(0, 12)}…` : ''}
            </option>
          ))}
        </select>

        <fieldset className={styles.modes}>
          <legend>Режим</legend>
          {modeOptions.map((option) => (
            <label key={option.value} className={styles.modeOption}>
              <input type="radio" name="task-mode" value={option.value} checked={mode === option.value}
                onChange={() => { touchDraft(); setMode(option.value); }} />
              <span>
                <strong>{option.label}</strong>
                <small>{option.hint}</small>
              </span>
            </label>
          ))}
        </fieldset>

        <label htmlFor="task-idea">Текст идеи</label>
        <textarea id="task-idea" value={idea} onChange={(event) => { touchDraft(); setIdea(event.target.value); }}
          placeholder="Опишите идею своими словами: какого результата хотите достичь и какие ограничения важно учесть."
          rows={5} required />

        <label htmlFor="task-links">Внешние ссылки</label>
        <textarea id="task-links" value={links} onChange={(event) => { touchDraft(); setLinks(event.target.value); }}
          placeholder={'ключ трекера или URL, по одной на строку\nнапример, JIRA-123 https://tracker.example/JIRA-123'}
          rows={3} />

        <div className={styles.actions}>
          <button type="submit" disabled={submitting || templates.length > 0 && !template}>
            {submitting ? 'Создаём…' : 'Создать задачу'}
          </button>
          {status && <span className={styles.status} role="status">{status}</span>}
          {createdRunID && <Link to={`/tasks/${encodeURIComponent(createdRunID)}`}>Открыть задачу</Link>}
        </div>
        {error && <p className={styles.error} role="alert">{error}</p>}
      </form>
    </main>
  );
}
