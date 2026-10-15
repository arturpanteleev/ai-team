import { useEffect, useState } from 'react';
import { BrowserRouter, Routes } from './router';
import { Layout } from './components/Layout';
import { Dashboard } from './pages/Dashboard';
import { ArtifactViewer } from './pages/ArtifactViewer';
import { Login } from './pages/Login';
import { Team } from './pages/Team';
import { McpAccess } from './pages/McpAccess';
import { TemplateEditor } from './pages/TemplateEditor';
import { Tasks } from './pages/Tasks';
import { NewTask } from './pages/NewTask';
import { Projects } from './pages/Projects';
import { EventFeed } from './pages/EventFeed';
import { PipelineRedirect, RunRedirect } from './pages/RunRedirect';
import { TaskDetail } from './pages/TaskDetail';
import { Observability } from './pages/Observability';
import { activateTeamInvitation, getAuthConfig, openSession, SESSION_EXPIRED_EVENT } from './api';

function RoutedApp({ teamManagementEnabled }: { teamManagementEnabled: boolean }) {
  return (
    <Layout teamManagementEnabled={teamManagementEnabled}>
      <Routes routes={[
        { path: '/', element: <Dashboard /> },
        { path: '/tasks', element: <Tasks /> },
        { path: '/tasks/new', element: <NewTask /> },
        { path: '/projects', element: <Projects /> },
        { path: '/tasks/:runID', element: <TaskDetail /> },
        { path: '/events', element: <EventFeed /> },
        { path: '/observability', element: <Observability /> },
        { path: '/runs/:runID', element: <RunRedirect /> },
        { path: '/team', element: teamManagementEnabled ? <Team /> : <main role="status">Управление командой недоступно в локальном режиме.</main> },
        { path: '/access', element: teamManagementEnabled ? <McpAccess /> : <main role="status">Персональные MCP-токены доступны только на сервере с облачной аутентификацией.</main> },
        { path: '/template', element: <TemplateEditor /> },
        { path: '/pipelines/:id', element: <PipelineRedirect /> },
        { path: '/artifacts/*', element: <ArtifactViewer /> },
      ]} fallback={<main role="status">Страница не найдена.</main>} />
    </Layout>
  );
}

function App() {
  const [authState, setAuthState] = useState<'loading' | 'login' | 'ready'>('loading');
  const [teamManagementEnabled, setTeamManagementEnabled] = useState(false);
  const [ssoEnabled, setSSOEnabled] = useState(false);

  useEffect(() => {
    const onSessionExpired = () => setAuthState('login');
    window.addEventListener(SESSION_EXPIRED_EVENT, onSessionExpired);
    void (async () => {
      try {
        const config = await getAuthConfig();
        setTeamManagementEnabled(config.team_management_enabled);
        setSSOEnabled(config.sso_enabled === true);
      } catch {
        setTeamManagementEnabled(false);
      }
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
    return <Login allowActivation={teamManagementEnabled} ssoEnabled={ssoEnabled} onLogin={async (token) => {
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
      <RoutedApp teamManagementEnabled={teamManagementEnabled} />
    </BrowserRouter>
  );
}

export default App;
