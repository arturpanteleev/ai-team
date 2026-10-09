import { useEffect, useState } from 'react';
import { BrowserRouter, Routes } from './router';
import { Layout } from './components/Layout';
import { Dashboard } from './pages/Dashboard';
import { PipelineDetail } from './pages/PipelineDetail';
import { ArtifactViewer } from './pages/ArtifactViewer';
import { Login } from './pages/Login';
import { Team } from './pages/Team';
import { activateTeamInvitation, openSession, SESSION_EXPIRED_EVENT } from './api';

function RoutedApp() {
  return (
    <Layout>
      <Routes routes={[
        { path: '/', element: <Dashboard /> },
        { path: '/team', element: <Team /> },
        { path: '/pipelines/:id', element: <PipelineDetail /> },
        { path: '/artifacts/*', element: <ArtifactViewer /> },
      ]} fallback={<main role="status">Страница не найдена.</main>} />
    </Layout>
  );
}

function App() {
  const [authState, setAuthState] = useState<'loading' | 'login' | 'ready'>('loading');

  useEffect(() => {
    const onSessionExpired = () => setAuthState('login');
    window.addEventListener(SESSION_EXPIRED_EVENT, onSessionExpired);
    void (async () => {
      try {
        await openSession();
        setAuthState('ready');
      } catch {
        setAuthState('login');
      }
    })();
    return () => window.removeEventListener(SESSION_EXPIRED_EVENT, onSessionExpired);
  }, []);

  if (authState === 'loading') return <div>Загрузка…</div>;
  if (authState === 'login') {
    return <Login onLogin={async (token) => {
      await openSession(token);
      setAuthState('ready');
    }} onActivate={async (token) => {
      const result = await activateTeamInvitation(token);
      await openSession(result.access_token);
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
