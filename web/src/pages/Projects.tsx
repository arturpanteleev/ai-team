import { useState } from 'react';
import type { FormEvent } from 'react';
import { createProject, getActivePrincipal, setProjectArchived } from '../api';
import { mayPerform } from '../permissions';
import { Link } from '../router';
import { usePageTitle } from '../hooks/usePageTitle';
import { useProjects } from '../hooks/useProjects';
import type { Project } from '../types';
import styles from './Projects.module.css';

export function Projects() {
  usePageTitle('Проекты');
  const principal = getActivePrincipal();
  // Создавать и архивировать проекты могут Product Owner и архитектор (как и
  // создавать задачи); окончательное решение за сервером. Без principal форма
  // доступна только в локальном режиме.
  const canManage = mayPerform(principal, 'project:manage');
  const { projects, loaded, error: loadError, reload } = useProjects();
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [status, setStatus] = useState('');
  const [error, setError] = useState('');

  const active = projects.filter((project) => !project.archived);
  const archived = projects.filter((project) => project.archived);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSubmitting(true);
    setError('');
    setStatus('');
    try {
      const project = await createProject(name.trim(), description.trim());
      setStatus(`Проект «${project.name}» создан.`);
      setName('');
      setDescription('');
      await reload();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Не удалось создать проект.');
    } finally {
      setSubmitting(false);
    }
  };

  const toggleArchive = async (project: Project) => {
    setError('');
    setStatus('');
    try {
      await setProjectArchived(project.id, !project.archived);
      await reload();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Не удалось изменить проект.');
    }
  };

  const card = (project: Project) => (
    <li key={project.id} className={styles.card} data-archived={project.archived}>
      <div className={styles.cardText}>
        <h3>{project.name}</h3>
        {project.description && <p>{project.description}</p>}
      </div>
      <div className={styles.cardActions}>
        <Link to={`/tasks?project=${project.id}`}>Открыть доску</Link>
        {canManage && (
          <button type="button" onClick={() => void toggleArchive(project)}
            aria-label={`${project.archived ? 'Вернуть из архива' : 'Архивировать'}: ${project.name}`}>
            {project.archived ? 'Вернуть из архива' : 'Архивировать'}
          </button>
        )}
      </div>
    </li>
  );

  return (
    <main className={styles.container}>
      <header className={styles.header}>
        <h1>Проекты</h1>
        <p>Проект — папка для задач. Каждая задача принадлежит ровно одному проекту; архивный проект не принимает новые задачи, но его история остаётся доступной.</p>
      </header>

      {loadError && <p role="alert" className={styles.error}>{loadError}</p>}

      {canManage && (
        <form className={styles.form} onSubmit={submit} aria-labelledby="project-create-heading">
          <h2 id="project-create-heading">Новый проект</h2>
          <label htmlFor="project-name">Название</label>
          <input id="project-name" value={name} onChange={(event) => setName(event.target.value)}
            placeholder="например, Платформа" maxLength={120} required />
          <label htmlFor="project-description">Описание</label>
          <textarea id="project-description" value={description} onChange={(event) => setDescription(event.target.value)}
            placeholder="О чём этот проект (необязательно)" rows={3} maxLength={2000} />
          <div className={styles.actions}>
            <button type="submit" disabled={submitting || !name.trim()}>
              {submitting ? 'Создаём…' : 'Создать проект'}
            </button>
            {status && <span className={styles.status} role="status">{status}</span>}
          </div>
        </form>
      )}
      {error && <p role="alert" className={styles.error}>{error}</p>}

      {!loaded ? (
        <p className={styles.muted}>Загружаем проекты…</p>
      ) : (
        <>
          <section aria-labelledby="projects-active-heading">
            <h2 id="projects-active-heading">Активные</h2>
            {active.length === 0
              ? <p className={styles.muted}>Активных проектов нет.{canManage ? ' Создайте проект выше, чтобы заводить задачи.' : ''}</p>
              : <ul className={styles.list}>{active.map(card)}</ul>}
          </section>
          {archived.length > 0 && (
            <section aria-labelledby="projects-archived-heading">
              <h2 id="projects-archived-heading">Архив ({archived.length})</h2>
              <ul className={styles.list}>{archived.map(card)}</ul>
            </section>
          )}
        </>
      )}
    </main>
  );
}
