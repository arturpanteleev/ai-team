import { actionVerbs } from '../actionLabels';
import { attemptOutcome, outcomeOf } from '../gateView';
import type { GateStage, GateStatus, GateWave, TaskGates } from '../types';
import { Icon } from './Icon';
import styles from './GatePanel.module.css';

// Gate state of one stage: the real gates of the current result version, the
// advisory warnings (amber), an administrator override of a failure (red) and
// the aggregated "may the task move on, and why". An override never turns a
// failed gate into a pass: the outcome label of the gate stays what it was.
// Red means a real failure or a bypass; amber only a recommendation.

const stateLabels: Record<string, string> = {
  passed: 'Переход разрешён',
  passed_with_warnings: 'Переход разрешён, есть замечания рекомендательных гейтов',
  overridden: 'Переход разрешён администратором несмотря на провал',
  legacy_overridden: 'Legacy: задача прошла по обходу до введения привязки к версии (только история)',
  blocked: 'Переход заблокирован: обязательный гейт не прошёл',
  error: 'Переход заблокирован: технический сбой гейта',
  waiting: 'Переход ждёт завершения или решения',
  incomplete: 'Переход заблокирован: не все обязательные гейты запускались',
};

function gateTone(status: GateStatus): 'fail' | 'warning' | undefined {
  if (status.warning) return 'warning';
  const outcome = attemptOutcome(status.attempt);
  return outcome.tone === 'fail' || outcome.tone === 'technical' ? 'fail' : undefined;
}

function when(value: string): string {
  return new Date(value).toLocaleString('ru-RU');
}

function overriddenList(wave: GateWave): string {
  const gates = wave.override?.gates ?? [];
  return gates.map((gate) => `${gate.gate_name} — ${outcomeOf(gate.execution, gate.verdict).label.toLowerCase()}`).join(', ') || '—';
}

export function OverrideBanner({ wave }: { wave: GateWave }) {
  const override = wave.override;
  if (!override) return null;
  return <p className={`${styles.banner} ${styles.override}`} role="status" data-testid="gate-override-banner">
    <Icon name="override" size={15} className={styles.bannerIcon} />
    <span>Обход провала: администратор {override.actor_id} разрешил переход {when(override.at)}.
      {' '}Причина: {override.reason}. Исход гейтов не изменён: {overriddenList(wave)}.
      {' '}Действует только для версии результата <code>{override.artifact_sha256.slice(0, 12)}</code>.</span>
  </p>;
}

export function AdvisoryBanner({ wave }: { wave: GateWave }) {
  const warnings = wave.gates.filter((gate) => gate.warning);
  if (warnings.length === 0) return null;
  const items = warnings.map((gate) => `${gate.gate_name} (${attemptOutcome(gate.attempt).label.toLowerCase()})`);
  return <p className={`${styles.banner} ${styles.advisory}`} role="status" data-testid="gate-advisory-banner">
    <Icon name="warning" size={15} className={styles.bannerIcon} />
    <span>Рекомендация не выполнена: {items.join(', ')}. Переход ими не блокируется.</span>
  </p>;
}

// Gates that can be repeated on the same result version (technical error or
// timeout). The buttons belong to the actions column, not to the status.
export function GateRetryActions({ stage, working, onRetry }: {
  stage: GateStage | undefined;
  working: boolean;
  onRetry: (gate: string) => void;
}) {
  const retryable = stage?.current?.gates.filter((status) => status.retryable) ?? [];
  if (retryable.length === 0) return null;
  return <>
    {retryable.map((status) => <button key={status.gate_name} type="button" disabled={working}
      title="Повтор на той же версии результата: успешные гейты не запускаются заново"
      onClick={() => onRetry(status.gate_name)}>
      {actionVerbs.retryGate}: {status.gate_name}
    </button>)}
  </>;
}

// compact — the short list for the status column; the full panel (with the
// attempt history) lives in the "Гейты" tab of the main area.
export function GatePanel({ stage, compact = false }: {
  stage: GateStage | undefined;
  compact?: boolean;
}) {
  const wave = stage?.current;
  if (!stage || !wave) return null;
  return <section className={styles.panel} aria-label="Гейты этапа" data-testid="gate-panel" data-compact={compact ? 'true' : undefined}>
    <h3>{compact ? 'Что проверяется' : 'Гейты этапа'}</h3>
    <OverrideBanner wave={wave} />
    <AdvisoryBanner wave={wave} />
    <p className={styles.decision} data-allowed={wave.transition.allowed ? 'true' : 'false'} data-state={wave.transition.state}>
      {stateLabels[wave.transition.state] ?? wave.transition.state}
      {wave.transition.reasons.length > 0 && <> · {wave.transition.reasons.join(' · ')}</>}
    </p>
    <ul className={styles.gates}>
      {wave.gates.map((status) => {
        const outcome = attemptOutcome(status.attempt);
        return <li key={status.gate_name} className={styles.gate} data-tone={gateTone(status)} data-overridden={status.overridden ? 'true' : undefined}>
          <span className={styles.name}>{status.gate_name}</span>
          <span className={styles.tag}>{status.mode === 'advisory' ? 'Рекомендательный' : 'Обязательный'}</span>
          <span className={`${styles.tag} ${styles[outcome.tone]}`}>{outcome.label}</span>
          {status.attempts > 1 && <span className={styles.tag}>попыток: {status.attempts}</span>}
          {status.warning && <span className={`${styles.tag} ${styles.warningTag}`}>Рекомендация</span>}
          {status.overridden && <span className={`${styles.tag} ${styles.overriddenTag}`}>Обойдён администратором, исход прежний</span>}
          {!compact && status.attempt?.reason && <p className={styles.reason}>{status.attempt.reason}</p>}
        </li>;
      })}
    </ul>
    {!compact && wave.attempts.length > wave.gates.length && <details>
      <summary>История попыток ({wave.attempts.length})</summary>
      <ol className={styles.history}>
        {wave.attempts.map((attempt) => <li key={attempt.attempt_id}>
          {attempt.gate_name}: {attemptOutcome(attempt).label} · {when(attempt.started_at)}
          {attempt.reason ? ` · ${attempt.reason}` : ''}
        </li>)}
      </ol>
    </details>}
  </section>;
}

// History of every override and every recommendation that did not pass, over
// all result versions of all stages.
export function GateHistory({ gates }: { gates: TaskGates | null | undefined }) {
  const entries: { key: string; kind: 'override' | 'advisory'; text: string }[] = [];
  for (const stage of gates?.stages ?? []) {
    for (const wave of stage.waves) {
      const label = `${stage.title || stage.stage_id} · версия ${wave.artifact_sha256.slice(0, 8)}`;
      if (wave.override) {
        entries.push({
          key: `o:${stage.stage_id}:${wave.stage_attempt_id}:${wave.artifact_sha256}`, kind: 'override',
          text: `${label}: обход провала администратором ${wave.override.actor_id}, ${when(wave.override.at)}. Причина: ${wave.override.reason}. Гейты: ${overriddenList(wave)}.`,
        });
      }
      for (const gate of wave.gates.filter((item) => item.warning)) {
        entries.push({
          key: `a:${stage.stage_id}:${wave.stage_attempt_id}:${wave.artifact_sha256}:${gate.gate_name}`, kind: 'advisory',
          text: `${label}: рекомендательный гейт ${gate.gate_name} — ${attemptOutcome(gate.attempt).label.toLowerCase()}${gate.attempt?.reason ? ` (${gate.attempt.reason})` : ''}.`,
        });
      }
    }
  }
  if (entries.length === 0) return <p>Обходов и невыполненных рекомендаций нет.</p>;
  return <ul className={styles.list}>
    {entries.map((entry) => <li key={entry.key} className={`${styles.banner} ${entry.kind === 'override' ? styles.override : styles.advisory}`}
      data-kind={entry.kind}>
      <Icon name={entry.kind === 'override' ? 'override' : 'warning'} size={15} className={styles.bannerIcon} />
      <span>{entry.text}</span>
    </li>)}
  </ul>;
}
