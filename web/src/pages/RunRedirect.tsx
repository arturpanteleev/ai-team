import { useEffect, useState } from 'react';
import { getTaskRun } from '../api';
import { useNavigate, useParams } from '../router';

export function RunRedirect() {
  const { runID } = useParams<{ runID: string }>();
  const navigate = useNavigate();
  const [error, setError] = useState('');

  useEffect(() => {
    let active = true;
    void getTaskRun(runID ?? '')
      .then((details) => {
        if (active) navigate(`/pipelines/${details.run.id}`, { replace: true });
      })
      .catch(() => {
        if (active) setError('Не удалось открыть задачу. Возможно, её уже удалили.');
      });
    return () => {
      active = false;
    };
  }, [navigate, runID]);

  return <p role={error ? 'alert' : 'status'}>{error || 'Открываем задачу…'}</p>;
}
