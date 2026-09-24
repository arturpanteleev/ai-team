import { useEffect, useState } from 'react';
import { BrowserRouter, useLocation } from './router';
import { Layout } from './components/Layout';
import { Dashboard } from './pages/Dashboard';
import { PipelineDetail } from './pages/PipelineDetail';
import { ArtifactViewer } from './pages/ArtifactViewer';
import { Login } from './pages/Login';
import { consumeBootstrapToken, getAuthConfig, openSession } from './api';

function RoutedApp() {
  const { pathname } = useLocation();
  let page = null;
  if (pathname === '/') {
    page = <Dashboard />;
  } else if (/^\/pipelines\/[^/]+$/.test(pathname)) {
    page = <PipelineDetail />;
  } else if (pathname.startsWith('/artifacts/')) {
    page = <ArtifactViewer />;
  }

  return <Layout>{page}</Layout>;
}

function App() {
  const [authState, setAuthState] = useState<'loading' | 'login' | 'ready'>('loading');

  useEffect(() => {
    void (async () => {
      try {
        const config = await getAuthConfig();
        if (!config.authentication_required) {
          await openSession();
          setAuthState('ready');
          return;
        }
        // Локальный режим: token приходит во fragment ссылки, которую
        // напечатал `ai-team web`. Cloud-режим: token вводится на экране
        // входа. После reload вкладки token не нужен — session-cookie уже
        // установлена, и openSession() без Bearer перевыпускает CSRF.
        const bootstrap = consumeBootstrapToken();
        await openSession(bootstrap || undefined);
        setAuthState('ready');
      } catch {
        setAuthState('login');
      }
    })();
  }, []);

  if (authState === 'loading') return <div>Загрузка…</div>;
  if (authState === 'login') {
    return <Login onLogin={async (token) => {
      await openSession(token);
      setAuthState('ready');
    }} />;
  }
  return (
    <BrowserRouter>
      <RoutedApp />
    </BrowserRouter>
  );
}

export default App;
