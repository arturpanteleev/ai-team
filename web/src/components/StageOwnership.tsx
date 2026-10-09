import { useEffect, useState } from 'react';
import { takeStage } from '../api';
import type { StageOwner } from '../types';
import styles from './StageOwnership.module.css';

interface StageOwnershipProps {
  runId: string;
  stageId: string;
  actorId: string;
  owner?: StageOwner;
  canTake: boolean;
  onChanged: () => Promise<void>;
}

function elapsedLabel(startedAt: string, now: number): string {
  const elapsed = Math.max(0, Math.floor((now - new Date(startedAt).getTime()) / 1000));
  const days = Math.floor(elapsed / 86400);
  const hours = Math.floor((elapsed % 86400) / 3600);
  const minutes = Math.floor((elapsed % 3600) / 60);
  const seconds = elapsed % 60;
  if (days > 0) return `${days} д ${hours} ч`;
  if (hours > 0) return `${hours} ч ${minutes} мин`;
  if (minutes > 0) return `${minutes} мин ${seconds} с`;
  return `${seconds} с`;
}

export function StageOwnership({ runId, stageId, actorId, owner, canTake, onChanged }: StageOwnershipProps) {
  const [now, setNow] = useState(Date.now());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const ownsStage = owner?.actor_id === actorId;

  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  const take = async () => {
    setBusy(true);
    setError('');
    try {
      await takeStage(runId, stageId);
      await onChanged();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : 'Не удалось взять этап');
      // A response can be lost after the controller recorded the event. Read
      // the durable projection so a retry cannot reset the owner timer.
      try {
        await onChanged();
      } catch {
        // Keep the original command error visible; a later page refresh can
        // recover the durable ownership projection.
      }
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className={styles.panel} aria-labelledby="stage-owner-heading">
      <div>
        <h2 id="stage-owner-heading">Ответственный за этап «{stageId}»</h2>
        {owner
          ? <p aria-live="polite"><strong>{owner.actor_id}</strong> держит этап {elapsedLabel(owner.taken_at, now)} · с {new Date(owner.taken_at).toLocaleString('ru-RU')}</p>
          : <p>Этап пока никто не взял.</p>}
      </div>
      {canTake && <button type="button" onClick={take} disabled={busy || ownsStage}>
        {busy ? 'Сохраняем…' : ownsStage ? 'Вы держите этап' : owner ? 'Перехватить этап' : 'Взять этап'}
      </button>}
      {error && <p className={styles.error} role="alert">{error}</p>}
    </section>
  );
}
