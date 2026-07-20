import { useState, useEffect, useCallback } from 'react';
import { useParams, Link } from 'react-router-dom';
import type { PipelineRun, Stage, Artifact } from '../types';
import { getPipelineRun, getPipelineArtifacts } from '../api';
import { useWebSocket } from '../hooks/useWebSocket';
import { StatusBadge } from '../components/StatusBadge';
import { StageRow } from '../components/StageRow';
import styles from './PipelineDetail.module.css';

export function PipelineDetail() {
  const { id } = useParams<{ id: string }>();
  const [run, setRun] = useState<PipelineRun | null>(null);
  const [stages, setStages] = useState<Stage[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchData = useCallback(async () => {
    if (!id) return;
    try {
      const [pipelineData, artifactsData] = await Promise.all([
        getPipelineRun(Number(id)),
        getPipelineArtifacts(Number(id)),
      ]);
      setRun(pipelineData.run);
      setStages(pipelineData.stages);
      setArtifacts(artifactsData);
    } catch {
      setError('Failed to load pipeline');
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    fetchData();
  }, [fetchData]);

  useWebSocket({
    onEvent: (event) => {
      if (event.pipeline_id === Number(id)) {
        fetchData();
      }
    },
  });

  // CLI пишет в SQLite без WebSocket-пуша — активный run поллим
  useEffect(() => {
    if (run?.status !== 'running') return;
    const t = window.setInterval(fetchData, 5000);
    return () => window.clearInterval(t);
  }, [run?.status, fetchData]);

  if (loading) return <div className={styles.loading}>Loading...</div>;
  if (error || !run) return <div className={styles.error}>{error || 'Not found'}</div>;

  const duration = run.completed_at
    ? ((new Date(run.completed_at).getTime() - new Date(run.started_at).getTime()) / 1000).toFixed(1) + 's'
    : '—';

  const getArtifactsForStage = (stage: Stage) =>
    artifacts.filter((a) => a.path.includes(stage.attempt_id) || a.name.toLowerCase().includes(stage.agent_name));

  return (
    <div className={styles.container}>
      <Link to="/" className={styles.back}>← Назад</Link>

      <div className={styles.header}>
        <h1 className={styles.title}>{run.feature}</h1>
        <div className={styles.meta}>
          <span className={styles.identifier}>Run: {run.run_id}</span>
          <span>Started: {new Date(run.started_at).toLocaleString('ru-RU')}</span>
          <span>Duration: {duration}</span>
          <StatusBadge status={run.status} />
        </div>
      </div>

      <div className={styles.stages}>
        {stages.map((stage) => (
          <StageRow
            key={stage.id}
            stage={stage}
            artifacts={getArtifactsForStage(stage)}
          />
        ))}
      </div>
    </div>
  );
}
