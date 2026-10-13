import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Observability } from './Observability';
import type { FlowMetrics } from '../types/flowMetrics';

const { downloadFlowMetricsCsv, getFlowMetrics } = vi.hoisted(() => ({
  downloadFlowMetricsCsv: vi.fn(),
  getFlowMetrics: vi.fn(),
}));

vi.mock('../api', () => ({ downloadFlowMetricsCsv, getFlowMetrics }));

const metrics: FlowMetrics = {
  from: '2026-10-01T00:00:00Z',
  to: '2026-11-01T00:00:00Z',
  lead_time: { count: 2, median_ms: 60 * 60_000, p80_ms: 2 * 60 * 60_000 },
  cycle_time: { count: 1, median_ms: 30 * 60_000, p80_ms: 30 * 60_000 },
  by_stage: [{
    stage: 'implementation',
    attempts: 4,
    duration_ms: 90 * 60_000,
    human_attempts: 1,
    agent_attempts: 3,
    agent_share: 0.75,
    by_state: [{ execution: 'agent', decision: 'approved', outcome: 'accepted', attempts: 3, duration_ms: 45 * 60_000 }],
    tokens_input: 1200,
    tokens_output: 600,
    usage_known_attempts: 3,
    usage_unknown_attempts: 1,
    usage_attestation: 'partial',
    subscription_share_estimate: 4.5,
    subscription_currency: 'USD',
  }],
  overrides_by_stage: { review: 1 },
  skips_by_stage: { qa: 2 },
  returns_by_stage: { implementation: 1 },
  usage: {
    known_runs: 2,
    unknown_runs: 1,
    tokens_input: 1200,
    tokens_output: 600,
    attestation: 'partial',
    source: 'metrics.UsageEnvelope',
    subscription_share_estimate: 4.5,
    subscription_currency: 'USD',
    estimate_basis: 'monthly_subscription_weighted_by_attested_tokens',
  },
  agent_results: 3,
  agent_accepted_without_edit: 2,
  agent_accepted_without_edit_rate: 2 / 3,
};

describe('Observability', () => {
  afterEach(cleanup);

  beforeEach(() => {
    vi.clearAllMocks();
    getFlowMetrics.mockResolvedValue(metrics);
    downloadFlowMetricsCsv.mockResolvedValue(new Blob(['metric,value'], { type: 'text/csv' }));
  });

  it('shows project flow, tokens, subscription share, and unknown usage explicitly', async () => {
    render(<Observability />);

    expect(await screen.findByText('1 ч')).toBeInTheDocument();
    expect(screen.getByText(/^66,7/)).toBeInTheDocument();
    expect(screen.getAllByText('implementation').length).toBeGreaterThan(0);
    expect(screen.getByText('Входные токены').parentElement).toHaveTextContent(/1[\s\u00a0]200/);
    expect(screen.getAllByText(/4,50/).length).toBeGreaterThan(0);
    expect(screen.getAllByText('Без usage: 1').length).toBeGreaterThan(0);
    expect(screen.getByText('Обходы: 1 · пропуски: 2 · возвраты: 1')).toBeInTheDocument();
    expect(screen.queryByText(/стоимость токенов/i)).not.toBeInTheDocument();
  });

  it('applies the selected inclusive date range and exports that same period', async () => {
    const createObjectURL = vi.fn(() => 'blob:metrics');
    const revokeObjectURL = vi.fn();
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    Object.defineProperty(URL, 'createObjectURL', { configurable: true, value: createObjectURL });
    Object.defineProperty(URL, 'revokeObjectURL', { configurable: true, value: revokeObjectURL });
    render(<Observability />);
    await screen.findByRole('heading', { name: 'Разложение по этапам' });

    fireEvent.change(screen.getByLabelText('С'), { target: { value: '2026-10-01' } });
    fireEvent.change(screen.getByLabelText('По'), { target: { value: '2026-10-31' } });
    fireEvent.click(screen.getByRole('button', { name: 'Применить' }));
    await waitFor(() => expect(getFlowMetrics).toHaveBeenLastCalledWith('2026-10-01', '2026-11-01'));

    fireEvent.click(screen.getByRole('button', { name: 'Экспорт CSV' }));
    await waitFor(() => expect(downloadFlowMetricsCsv).toHaveBeenCalledWith('2026-10-01', '2026-11-01'));
    expect(click).toHaveBeenCalledTimes(1);
    click.mockRestore();
  });

  it('breaks overrides, skips, and returns down by stage, including stages without attempts', async () => {
    render(<Observability />);
    const table = (await screen.findByRole('heading', { name: 'Обходы, пропуски и возвраты по этапам' })).closest('section') as HTMLElement;

    const rows = within(table).getAllByRole('row').slice(1);
    expect(rows.map((row) => row.textContent)).toEqual(['implementation001', 'qa020', 'review100']);
    expect(screen.getByText(/accepted: 3 · 45 мин/)).toBeInTheDocument();
  });

  it('shows an explicit empty state for a period without tasks', async () => {
    getFlowMetrics.mockResolvedValue({
      ...metrics,
      lead_time: { count: 0 },
      cycle_time: { count: 0 },
      by_stage: [],
      overrides_by_stage: {},
      skips_by_stage: {},
      returns_by_stage: {},
      usage: { ...metrics.usage, known_runs: 0, unknown_runs: 0, tokens_input: null, tokens_output: null, subscription_share_estimate: null },
      agent_results: 0,
      agent_accepted_without_edit: 0,
      agent_accepted_without_edit_rate: null,
    });
    render(<Observability />);

    expect(await screen.findByText('За выбранный период задач и попыток этапов нет.')).toBeInTheDocument();
    expect(screen.getByText('Lead Time · медиана').parentElement).toHaveTextContent('нет данных');
    expect(screen.getByText('Входные токены').parentElement).toHaveTextContent('нет данных');
    expect(screen.getByText('За этот период данных по этапам нет.')).toBeInTheDocument();
    expect(screen.getByText('За этот период обходов, пропусков и возвратов не было.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Экспорт CSV' })).toBeDisabled();
  });

  it('labels the period fields as UTC and shows the applied period', async () => {
    render(<Observability />);
    await screen.findByRole('heading', { name: 'Разложение по этапам' });
    expect(screen.getByLabelText('С')).toHaveAccessibleDescription('Даты в UTC, обе границы включительно.');
    expect(screen.getByLabelText('По')).toHaveAccessibleDescription('Даты в UTC, обе границы включительно.');

    fireEvent.change(screen.getByLabelText('С'), { target: { value: '2026-10-01' } });
    fireEvent.change(screen.getByLabelText('По'), { target: { value: '2026-10-31' } });
    // Пока период не применён, подпись показывает прежний.
    expect(screen.getByTestId('applied-period')).not.toHaveTextContent('01.10.2026');
    fireEvent.click(screen.getByRole('button', { name: 'Применить' }));
    expect(screen.getByTestId('applied-period')).toHaveTextContent('Показан период: 01.10.2026 — 31.10.2026 (UTC)');
  });

  it('labels stage states with execution, decision and outcome', async () => {
    render(<Observability />);
    expect(await screen.findByText(/agent → approved → accepted: 3 · 45 мин/)).toBeInTheDocument();
  });

  it('shows skipped attempts separately from agent and human attempts', async () => {
    getFlowMetrics.mockResolvedValue({ ...metrics, by_stage: [{ ...metrics.by_stage[0], skipped_attempts: 1 }] });
    render(<Observability />);
    expect(await screen.findByText('3 агент · 1 человек · 1 пропуск')).toBeInTheDocument();
  });

  it('reports a load failure and drops metrics of the previous period', async () => {
    render(<Observability />);
    await screen.findByRole('heading', { name: 'Разложение по этапам' });
    getFlowMetrics.mockRejectedValueOnce(new Error('Метрики недоступны'));

    fireEvent.change(screen.getByLabelText('С'), { target: { value: '2026-09-01' } });
    fireEvent.change(screen.getByLabelText('По'), { target: { value: '2026-09-30' } });
    fireEvent.click(screen.getByRole('button', { name: 'Применить' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('Метрики недоступны');
    expect(screen.queryAllByText('implementation')).toHaveLength(0);
    expect(screen.getByRole('button', { name: 'Экспорт CSV' })).toBeDisabled();
  });

  it('reports a CSV export failure without downloading a file', async () => {
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined);
    downloadFlowMetricsCsv.mockRejectedValueOnce(new Error('Не удалось экспортировать метрики'));
    render(<Observability />);
    await screen.findByRole('heading', { name: 'Разложение по этапам' });

    fireEvent.click(screen.getByRole('button', { name: 'Экспорт CSV' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('Не удалось экспортировать метрики');
    expect(click).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Экспорт CSV' })).toBeEnabled();
    click.mockRestore();
  });

  it('rejects an inverted period without making another request', async () => {
    render(<Observability />);
    await screen.findByRole('heading', { name: 'Разложение по этапам' });
    const callCount = getFlowMetrics.mock.calls.length;
    fireEvent.change(screen.getByLabelText('С'), { target: { value: '2026-10-31' } });
    fireEvent.change(screen.getByLabelText('По'), { target: { value: '2026-10-01' } });
    fireEvent.click(screen.getByRole('button', { name: 'Применить' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('дата «По» должна быть не раньше');
    expect(getFlowMetrics).toHaveBeenCalledTimes(callCount);
  });
});
