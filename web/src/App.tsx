import { BrowserRouter, Routes, Route } from 'react-router-dom';
import { Layout } from './components/Layout';
import { Dashboard } from './pages/Dashboard';
import { PipelineDetail } from './pages/PipelineDetail';
import { ArtifactViewer } from './pages/ArtifactViewer';

function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/" element={<Layout />}>
          <Route index element={<Dashboard />} />
          <Route path="pipelines/:id" element={<PipelineDetail />} />
          <Route path="artifacts/*" element={<ArtifactViewer />} />
        </Route>
      </Routes>
    </BrowserRouter>
  );
}

export default App;