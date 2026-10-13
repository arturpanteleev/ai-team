import type { FlowStageStats } from './types/flowMetrics';

export function formatTokens(value: number | null | undefined): string {
  return value === null || value === undefined || !Number.isFinite(value)
    ? 'нет данных'
    : new Intl.NumberFormat('ru-RU').format(value);
}

// Токены этапа «вход / выход». Если usage подтверждён не у всех попыток агента,
// добавляется «частично (N из M)»: из M попыток агента подтверждены N.
export function stageTokensText(stats: FlowStageStats | undefined): string {
  const tokens = `${formatTokens(stats?.tokens_input)} / ${formatTokens(stats?.tokens_output)}`;
  if (!stats || stats.usage_attestation === 'complete' || stats.usage_attestation === 'not_applicable') return tokens;
  const total = stats.usage_known_attempts + stats.usage_unknown_attempts;
  return total > 0 ? `${tokens} — частично (${stats.usage_known_attempts} из ${total})` : tokens;
}
