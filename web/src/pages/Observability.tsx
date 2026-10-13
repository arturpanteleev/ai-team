import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { downloadFlowMetricsCsv, getFlowMetrics } from '../api';
import { usePageTitle } from '../hooks/usePageTitle';
import type { FlowMetrics, FlowStageStats, FlowTariffCost } from '../types/flowMetrics';
import styles from './Observability.module.css';

function utcDate(date: Date): string {
  return date.toISOString().slice(0, 10);
}

function defaultPeriod(): { from: string; to: string } {
  const today = new Date();
  const from = new Date(today);
  from.setUTCDate(from.getUTCDate() - 29);
  return { from: utcDate(from), to: utcDate(today) };
}

function toExclusiveDate(date: string): string {
  const value = new Date(`${date}T00:00:00.000Z`);
  value.setUTCDate(value.getUTCDate() + 1);
  return utcDate(value);
}

function formatDuration(milliseconds?: number): string {
  if (milliseconds === undefined || !Number.isFinite(milliseconds) || milliseconds < 0) return 'нет данных';
  const minutes = Math.round(milliseconds / 60_000);
  if (minutes < 60) return `${minutes} мин`;
  const hours = Math.floor(minutes / 60);
  const remainingMinutes = minutes % 60;
  if (hours < 48) return remainingMinutes ? `${hours} ч ${remainingMinutes} мин` : `${hours} ч`;
  const days = Math.floor(hours / 24);
  const remainingHours = hours % 24;
  return remainingHours ? `${days} д ${remainingHours} ч` : `${days} д`;
}

function formatTokens(value: number | null | undefined): string {
  return value === null || value === undefined || !Number.isFinite(value)
    ? 'нет данных'
    : new Intl.NumberFormat('ru-RU').format(value);
}

function formatEstimate(value: number | null | undefined, currency?: string): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return 'нет данных';
  try {
    return `≈ ${new Intl.NumberFormat('ru-RU', { style: 'currency', currency: currency || 'USD', maximumFractionDigits: 2 }).format(value)}`;
  } catch {
    return `≈ ${value.toFixed(2)} ${currency || ''}`.trim();
  }
}

function formatMoney(value: number, currency?: string): string {
  try {
    return new Intl.NumberFormat('ru-RU', { style: 'currency', currency: currency || 'USD', maximumFractionDigits: 2 }).format(value);
  } catch {
    return `${value.toFixed(2)} ${currency || ''}`.trim();
  }
}

const tariffReasons: Record<string, string> = {
  tariff_not_configured: 'тариф не задан',
  tariff_missing_for_model: 'нет тарифа для модели',
  model_unknown: 'модель попытки неизвестна',
  tariff_not_effective_at_attempt: 'тариф ещё не действовал на дату попытки',
  cache_price_missing: 'нет цены кешированных токенов',
  usage_unknown: 'нет подтверждённого usage',
  usage_invalid: 'некорректный usage',
  mixed_currency: 'тарифы в разных валютах',
  no_agent_attempts: 'нет попыток агента',
};

// Стоимость по тарифу показывается только целиком: неизвестная модель или
// незаданный тариф дают «нет данных», а не ноль.
function formatTariffCost(cost: FlowTariffCost | undefined): string {
  if (!cost || cost.amount === null || !Number.isFinite(cost.amount)) return 'нет данных';
  return formatMoney(cost.amount, cost.currency);
}

function tariffCostNote(cost: FlowTariffCost | undefined): string {
  if (!cost) return 'тариф не задан';
  if (cost.amount !== null && Number.isFinite(cost.amount)) {
    const dates = (cost.tariff_dates ?? []).map(formatDay).join(', ');
    return dates ? `по тарифу, дата тарифа: ${dates}` : 'по тарифу';
  }
  const reason = tariffReasons[cost.reason ?? ''] ?? 'нет данных';
  const models = cost.reason === 'tariff_missing_for_model' && cost.unpriced_models?.length ? `: ${cost.unpriced_models.join(', ')}` : '';
  const total = cost.priced_attempts + cost.unpriced_attempts;
  const counted = total > 0 && cost.priced_attempts > 0 ? ` (оценено ${cost.priced_attempts} из ${total})` : '';
  return `${reason}${models}${counted}`;
}

function formatPercent(value: number | null | undefined): string {
  return value === null || value === undefined || !Number.isFinite(value)
    ? 'нет данных'
    : new Intl.NumberFormat('ru-RU', { style: 'percent', maximumFractionDigits: 1 }).format(value);
}

function sumCounts(values: Record<string, number> | undefined): number {
  return Object.values(values ?? {}).reduce((sum, count) => sum + count, 0);
}

interface StageActions {
  stage: string;
  overrides: number;
  skips: number;
  returns: number;
}

// Обходы, пропуски и возвраты приходят отдельными словарями по этапам; этап
// может встречаться в них, даже если за период у него не было попыток.
function stageActions(metrics: FlowMetrics): StageActions[] {
  const known = metrics.by_stage.map((stage) => stage.stage);
  const extra = new Set<string>();
  for (const counts of [metrics.overrides_by_stage, metrics.skips_by_stage, metrics.returns_by_stage]) {
    for (const stage of Object.keys(counts ?? {})) {
      if (!known.includes(stage)) extra.add(stage);
    }
  }
  return [...known, ...[...extra].sort()]
    .map((stage) => ({
      stage,
      overrides: metrics.overrides_by_stage?.[stage] ?? 0,
      skips: metrics.skips_by_stage?.[stage] ?? 0,
      returns: metrics.returns_by_stage?.[stage] ?? 0,
    }))
    .filter((row) => row.overrides + row.skips + row.returns > 0);
}

function formatDay(date: string): string {
  const [year, month, day] = date.split('-');
  return year && month && day ? `${day}.${month}.${year}` : date;
}

// Подпись состояния: исполнение, решение и результат попыток — без «unknown».
function stateLabel(state: { execution: string; decision: string; outcome: string }): string {
  return [state.execution, state.decision, state.outcome]
    .filter((value) => value && value !== 'unknown')
    .join(' → ') || 'нет данных';
}

function isEmptyPeriod(metrics: FlowMetrics): boolean {
  return metrics.lead_time.count === 0 && metrics.cycle_time.count === 0 && metrics.by_stage.length === 0 &&
    stageActions(metrics).length === 0 && metrics.agent_results === 0;
}

function StageUsage({ stage }: { stage: FlowStageStats }) {
  return (
    <div className={styles.usageDetail}>
      <span>Вход: {formatTokens(stage.tokens_input)}</span>
      <span>Выход: {formatTokens(stage.tokens_output)}</span>
      <span title={tariffCostNote(stage.cost_by_tariff)}>По тарифу: {formatTariffCost(stage.cost_by_tariff)}</span>
      <span>Доля подписки (оценка): {formatEstimate(stage.subscription_share_estimate, stage.subscription_currency)}</span>
      {stage.usage_unknown_attempts > 0 && <span className={styles.muted}>Без usage: {stage.usage_unknown_attempts} этап(а)</span>}
    </div>
  );
}

function downloadBlob(blob: Blob) {
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = 'flow-metrics.csv';
  document.body.append(link);
  link.click();
  link.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}

export function Observability() {
  usePageTitle('Наблюдаемость');
  const [initialPeriod] = useState(defaultPeriod);
  const [draftFrom, setDraftFrom] = useState(initialPeriod.from);
  const [draftTo, setDraftTo] = useState(initialPeriod.to);
  const [period, setPeriod] = useState(initialPeriod);
  const [metrics, setMetrics] = useState<FlowMetrics | null>(null);
  const [loading, setLoading] = useState(true);
  const [exporting, setExporting] = useState(false);
  const [error, setError] = useState('');
  const generation = useRef(0);

  useEffect(() => {
    const current = ++generation.current;
    setLoading(true);
    setError('');
    void getFlowMetrics(period.from, toExclusiveDate(period.to))
      .then((value) => {
        if (generation.current === current) setMetrics(value);
      })
      .catch((reason: unknown) => {
        if (generation.current !== current) return;
        // Не оставляем на экране цифры прошлого периода под новым периодом.
        setMetrics(null);
        setError(reason instanceof Error ? reason.message : 'Не удалось загрузить метрики');
      })
      .finally(() => {
        if (generation.current === current) setLoading(false);
      });
    return () => {
      generation.current += 1;
    };
  }, [period]);

  const applyPeriod = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!draftFrom || !draftTo || draftTo < draftFrom) {
      setError('Укажите корректный период: дата «По» должна быть не раньше даты «С».');
      return;
    }
    setPeriod({ from: draftFrom, to: draftTo });
  };

  const exportCsv = async () => {
    setExporting(true);
    setError('');
    try {
      downloadBlob(await downloadFlowMetricsCsv(period.from, toExclusiveDate(period.to)));
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : 'Не удалось экспортировать метрики');
    } finally {
      setExporting(false);
    }
  };

  const usage = metrics?.usage;
  const acceptedRate = metrics?.agent_accepted_without_edit_rate;
  const overrideCount = sumCounts(metrics?.overrides_by_stage);
  const skipCount = sumCounts(metrics?.skips_by_stage);
  const returnCount = sumCounts(metrics?.returns_by_stage);
  const actions = metrics ? stageActions(metrics) : [];
  // Экспорт только для успешно загруженного непустого периода.
  const canExport = !loading && !exporting && metrics !== null && !isEmptyPeriod(metrics);

  return (
    <div className={styles.container}>
      <header className={styles.header}>
        <div>
          <h1 className={styles.title}>Наблюдаемость</h1>
          <p className={styles.subtitle}>Сводка по задачам проекта и выбранному периоду.</p>
        </div>
        <button type="button" className={styles.export} onClick={() => void exportCsv()} disabled={!canExport}
          title={canExport ? undefined : 'Нет загруженных данных за период'}>
          {exporting ? 'Готовим CSV…' : 'Экспорт CSV'}
        </button>
      </header>

      <form className={styles.period} onSubmit={applyPeriod} aria-label="Период метрик">
        <label>С<input type="date" value={draftFrom} aria-describedby="period-utc-hint" onChange={(event) => setDraftFrom(event.currentTarget.value)} /></label>
        <label>По<input type="date" value={draftTo} aria-describedby="period-utc-hint" onChange={(event) => setDraftTo(event.currentTarget.value)} /></label>
        <button type="submit">Применить</button>
        <small id="period-utc-hint" className={styles.muted}>Даты в UTC, обе границы включительно.</small>
      </form>
      <p className={styles.muted} data-testid="applied-period">Показан период: {formatDay(period.from)} — {formatDay(period.to)} (UTC)</p>

      {error && <p className={styles.error} role="alert">{error}</p>}
      {loading ? <p className={styles.notice} role="status">Загружаем метрики…</p> : metrics && (
        <>
          {isEmptyPeriod(metrics) && <p className={styles.notice} role="status">За выбранный период задач и попыток этапов нет.</p>}
          <section className={styles.cards} aria-label="Ключевые показатели">
            <article className={styles.card}><span>Lead Time · медиана</span><strong>{formatDuration(metrics.lead_time.median_ms)}</strong><small>p80: {formatDuration(metrics.lead_time.p80_ms)} · задач: {metrics.lead_time.count}</small></article>
            <article className={styles.card}><span>Cycle Time · медиана</span><strong>{formatDuration(metrics.cycle_time.median_ms)}</strong><small>p80: {formatDuration(metrics.cycle_time.p80_ms)} · задач: {metrics.cycle_time.count}</small></article>
            <article className={styles.card}><span>Принято без правок</span><strong>{formatPercent(acceptedRate)}</strong><small>{metrics.agent_accepted_without_edit} из {metrics.agent_results} результатов агента</small></article>
            <article className={styles.card}><span>Действия с этапами</span><strong>{overrideCount + skipCount + returnCount}</strong><small>Обходы: {overrideCount} · пропуски: {skipCount} · возвраты: {returnCount}</small></article>
          </section>

          <section className={styles.usage} aria-labelledby="usage-title">
            <div className={styles.sectionHeader}>
              <div><h2 id="usage-title">Токены и стоимость</h2><p>Стоимость по тарифу — подтверждённые токены попыток агента × цена модели из тарифов проекта. Доля подписки — отдельная приблизительная оценка, не тарификация провайдера.</p></div>
              {usage && usage.unknown_runs > 0 && <span className={styles.badge}>Без usage: {usage.unknown_runs}</span>}
            </div>
            <div className={styles.usageTotals}>
              <div><span>Входные токены</span><strong>{formatTokens(usage?.tokens_input)}</strong></div>
              <div><span>Выходные токены</span><strong>{formatTokens(usage?.tokens_output)}</strong></div>
              <div data-testid="tariff-cost"><span>Стоимость по тарифу</span><strong>{formatTariffCost(metrics.cost_by_tariff)}</strong><small className={styles.muted}>{tariffCostNote(metrics.cost_by_tariff)}</small></div>
              <div><span>Приблизительная доля подписки (оценка)</span><strong>{formatEstimate(usage?.subscription_share_estimate, usage?.subscription_currency)}</strong></div>
            </div>
            {usage?.subscription_share_estimate !== null && usage?.subscription_share_estimate !== undefined && usage.estimate_basis &&
              <small className={styles.muted}>Основа оценки: месячная подписка проекта, распределённая пропорционально подтверждённым токенам.</small>}
          </section>

          <section className={styles.stageSection} aria-labelledby="stage-title">
            <div className={styles.sectionHeader}><div><h2 id="stage-title">Разложение по этапам</h2><p>Доля агента считается среди непогашенных попыток без пропусков.</p></div></div>
            {!metrics.by_stage.length ? <p className={styles.notice}>За этот период данных по этапам нет.</p> : (
              <div className={styles.tableWrap}>
                <table>
                  <thead><tr><th>Этап</th><th>Попытки</th><th>Время</th><th>Доля агента</th><th>Решения этапа</th><th>Использование</th></tr></thead>
                  <tbody>{metrics.by_stage.map((stage) => (
                    <tr key={stage.stage}>
                      <th scope="row">{stage.stage}</th>
                      <td>{stage.attempts}<small>{stage.agent_attempts} агент · {stage.human_attempts} человек{
                        stage.skipped_attempts ? ` · ${stage.skipped_attempts} пропуск` : ''}</small></td>
                      <td>{formatDuration(stage.duration_ms)}</td>
                      <td>{formatPercent(stage.agent_share)}</td>
                      <td>{stage.by_state.length ? stage.by_state.map((state) => (
                        <span className={styles.state} key={`${state.execution}:${state.decision}:${state.outcome}`}>
                          {stateLabel(state)}: {state.attempts} · {formatDuration(state.duration_ms)}
                        </span>
                      )) : <span className={styles.muted}>нет данных</span>}</td>
                      <td><StageUsage stage={stage} /></td>
                    </tr>
                  ))}</tbody>
                </table>
              </div>
            )}
          </section>

          <section className={styles.stageSection} aria-labelledby="actions-title">
            <div className={styles.sectionHeader}><div><h2 id="actions-title">Обходы, пропуски и возвраты по этапам</h2><p>Ручные действия с этапами за период.</p></div></div>
            {actions.length === 0 ? <p className={styles.notice}>За этот период обходов, пропусков и возвратов не было.</p> : (
              <div className={styles.tableWrap}>
                <table>
                  <thead><tr><th>Этап</th><th>Обходы</th><th>Пропуски</th><th>Возвраты</th></tr></thead>
                  <tbody>{actions.map((row) => (
                    <tr key={row.stage}>
                      <th scope="row">{row.stage}</th>
                      <td>{row.overrides}</td>
                      <td>{row.skips}</td>
                      <td>{row.returns}</td>
                    </tr>
                  ))}</tbody>
                </table>
              </div>
            )}
          </section>
        </>
      )}
    </div>
  );
}
