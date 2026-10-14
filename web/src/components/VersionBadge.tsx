import { useEffect, useState } from 'react';
import styles from './VersionBadge.module.css';

// The frontend identifies its own bundle: Vite names the entry script by content hash, so it changes on every rebuild.
function uiBuild(): string {
  const src = document.querySelector<HTMLScriptElement>('script[type="module"][src*="/assets/index-"]')?.getAttribute('src') ?? '';
  const match = /index-([A-Za-z0-9_-]+)\.js/.exec(src);
  return match ? match[1] : 'dev';
}

interface BackendBuild {
  revision: string;
  modified: boolean;
}

export function VersionBadge() {
  const [backend, setBackend] = useState<BackendBuild | null>(null);

  useEffect(() => {
    let active = true;
    fetch('/api/version', { credentials: 'same-origin' })
      .then((response) => (response.ok ? response.json() : null))
      .then((body: { backend?: BackendBuild } | null) => {
        if (active && body?.backend) setBackend(body.backend);
      })
      .catch(() => { /* the badge is informational; the page works without it */ });
    return () => { active = false; };
  }, []);

  const revision = backend?.revision ? backend.revision.slice(0, 7) : '—';
  const title = backend?.revision ? `Backend build ${backend.revision}${backend.modified ? ' (с локальными правками)' : ''}` : 'Версия бэкенда не получена';

  return (
    <div className={styles.badge} role="status" aria-label="Версии приложения" title={title}>
      <span>UI {uiBuild()}</span>
      <span aria-hidden="true">·</span>
      <span>API {revision}{backend?.modified ? '*' : ''}</span>
    </div>
  );
}
