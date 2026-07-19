import { useState, useEffect } from 'react';
import { useParams, Link, useSearchParams } from 'react-router-dom';
import ReactMarkdown from 'react-markdown';
import { getArtifact } from '../api';
import styles from './ArtifactViewer.module.css';

export function ArtifactViewer() {
  const { '*': path } = useParams<{ '*': string }>();
  const [searchParams, setSearchParams] = useSearchParams();
  const [content, setContent] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const view = searchParams.get('view') || 'rendered';
  const isRendered = view === 'rendered';

  useEffect(() => {
    if (!path) return;
    setLoading(true);
    getArtifact(path)
      .then((data) => setContent(data.content))
      .catch(() => setError('Failed to load artifact'))
      .finally(() => setLoading(false));
  }, [path]);

  if (loading) return <div className={styles.loading}>Loading...</div>;
  if (error || !content) return <div className={styles.error}>{error || 'Not found'}</div>;

  const toggleView = () => {
    setSearchParams({ view: isRendered ? 'raw' : 'rendered' });
  };

  return (
    <div className={styles.container}>
      <Link to={-1 as any} className={styles.back}>← Назад</Link>

      <div className={styles.header}>
        <span className={styles.path}>{decodeURIComponent(path || '')}</span>
        <button className={styles.toggle} onClick={toggleView}>
          {isRendered ? 'Raw' : 'Rendered'}
        </button>
      </div>

      <div className={styles.content}>
        {isRendered ? (
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