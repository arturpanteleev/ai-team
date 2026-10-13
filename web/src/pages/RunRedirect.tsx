import { useEffect } from 'react';
import { getPipelineRun } from '../api';
import { useNavigate, useParams } from '../router';

// Старые адреса задачи ведут на единый экран /tasks/:id.
// /runs/:runID уже содержит run_id; /pipelines/:id содержит числовой id, его нужно перевести в run_id.
export function RunRedirect() {
  const { runID = '' } = useParams<{ runID: string }>();
  const navigate = useNavigate();

  useEffect(() => {
    navigate(`/tasks/${encodeURIComponent(runID)}`, { replace: true });
  }, [navigate, runID]);

  return <p role="status">Открываем задачу…</p>;
}

export function PipelineRedirect() {
  const { id = '' } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const pipelineID = Number(id);

  useEffect(() => {
    let active = true;
    if (!Number.isInteger(pipelineID) || pipelineID <= 0) {
      navigate('/tasks', { replace: true });
      return undefined;
    }
    void getPipelineRun(pipelineID)
      .then((data) => {
        if (active) navigate(`/tasks/${encodeURIComponent(data.run.run_id)}`, { replace: true });
      })
      .catch(() => {
        if (active) navigate('/tasks', { replace: true });
      });
    return () => { active = false; };
  }, [navigate, pipelineID]);

  return <p role="status">Открываем задачу…</p>;
}
