import { useState, useEffect } from 'react';
import { useParams, useNavigate, useSearchParams } from '../router';
import ReactMarkdown from 'react-markdown';
import { createArtifactRevision, getArtifact, getArtifactRevisions } from '../api';
import type { ArtifactRevision } from '../types';
import styles from './ArtifactViewer.module.css';

export function ArtifactViewer() {
  const { '*': path } = useParams<{ '*': string }>();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const [content, setContent] = useState<string | null>(null);
  const [sourceContent, setSourceContent] = useState<string | null>(null);
  const [revisions, setRevisions] = useState<ArtifactRevision[]>([]);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState('');
  const [comment, setComment] = useState('');
  const [saving, setSaving] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const view = searchParams.get('view') || 'rendered';
  const isRendered = view === 'rendered';

  useEffect(() => {
    if (!path) return;
    const [runId, ...artifactParts] = path.split('/');
    if (!runId || artifactParts.length === 0) {
      setError('Invalid run-aware artifact path');
      setLoading(false);
      return;
    }
    setLoading(true);
    setError(null);
    const artifactPath = artifactParts.join('/');
    Promise.all([getArtifact(runId, artifactPath), getArtifactRevisions(runId, artifactPath)])
      .then(([data, history]) => {
        setSourceContent(data);
        setRevisions(history);
        const latest = history[history.length - 1];
        setContent(latest?.content ?? data);
        setDraft(latest?.content ?? data);
      })
      .catch(() => setError('Failed to load artifact'))
      .finally(() => setLoading(false));
  }, [path]);

  const startEditing = () => {
    setDraft(content ?? '');
    setEditing(true);
  };

  const saveRevision = async () => {
    if (!path || sourceContent == null) return;
    const [runId, ...artifactParts] = path.split('/');
    const previous = revisions[revisions.length - 1];
    setSaving(true);
    setError(null);
    try {
      const baseSHA = previous?.sha256 ?? await sha256(sourceContent);
      const revision = await createArtifactRevision(runId, {
        artifact_path: artifactParts.join('/'),
        base_revision: previous?.id,
        base_sha256: baseSHA,
        content: draft,
        comment,
      });
      setRevisions((current) => [...current, revision]);
      setContent(revision.content);
      setEditing(false);
      setComment('');
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Не удалось сохранить версию');
    } finally {
      setSaving(false);
    }
  };

  if (loading) return <div className={styles.loading}>Loading...</div>;
  if (error || !content) return <div className={styles.error}>{error || 'Not found'}</div>;

  const toggleView = () => {
    setSearchParams({ view: isRendered ? 'raw' : 'rendered' });
  };

  return (
    <div className={styles.container}>
      <button type="button" onClick={() => navigate(-1)} className={styles.back}>← Назад</button>

      <div className={styles.header}>
        <span className={styles.path}>{path || ''}</span>
        <div className={styles.toolbar}>
          {!editing && <button className={styles.toggle} onClick={startEditing}>Предложить изменение</button>}
          {!editing && <button className={styles.toggle} onClick={toggleView}>{isRendered ? 'Raw' : 'Rendered'}</button>}
        </div>
      </div>

      {revisions.length > 0 && <section className={styles.history} aria-label="История изменений">
        <h2>История человеческих версий</h2>
        {revisions.map((revision) => <article key={revision.id}>
          <strong>Версия {revision.revision}</strong> · {revision.actor_id} · {new Date(revision.created_at).toLocaleString('ru-RU')}
          <code>{revision.sha256}</code>
          {revision.comment && <p>{revision.comment}</p>}
          <button className={styles.toggle} onClick={() => {
            setDraft(revision.content);
            setComment(`Новая версия на основе ${revision.id}`);
            setEditing(true);
          }}>Создать новую версию на её основе</button>
        </article>)}
      </section>}

      <div className={styles.content}>
        {editing ? (
          <div className={styles.editor}>
            <label>Новая версия артефакта<textarea value={draft} onChange={(event) => setDraft(event.target.value)} rows={18} maxLength={10 * 1024 * 1024} /></label>
            <label>Комментарий<textarea value={comment} onChange={(event) => setComment(event.target.value)} rows={3} maxLength={16 * 1024} /></label>
            <button disabled={saving || (!draft.trim() && !comment.trim())} onClick={saveRevision}>{saving ? 'Сохранение…' : 'Сохранить версию'}</button>
            <button disabled={saving} onClick={() => setEditing(false)}>Отмена</button>
          </div>
        ) : isRendered ? (
          <div className={styles.markdown}>
            <ReactMarkdown>{content}</ReactMarkdown>
          </div>
        ) : (
          <pre className={styles.raw}>{content}</pre>
        )}
      </div>
    </div>
  );
}

async function sha256(value: string): Promise<string> {
  const bytes = new TextEncoder().encode(value);
  const hash = await crypto.subtle.digest('SHA-256', bytes);
  return Array.from(new Uint8Array(hash), (item) => item.toString(16).padStart(2, '0')).join('');
}
