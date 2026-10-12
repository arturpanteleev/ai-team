import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { TaskEvent, TaskRunDetails } from '../types';
import type { FlowMetrics } from '../types/flowMetrics';
import { TaskTimeline } from './TaskTimeline';

vi.mock('../api', () => ({ getTaskFlowMetrics: vi.fn() }));

const details: TaskRunDetails = {
  run: { id: 1, run_id: 'run-1', feature: 'Маршрут', status: 'completed', started_at: '2026-10-12T09:00:00Z', completed_at: '2026-10-12T10:00:00Z' },
  template: {
    valid: true, template: 'route', title: 'Маршрут', version: 'a'.repeat(64), returns: [],
    stages: [
      { id: 'analysis', title: 'Анализ', function: 'analyst', result: 'md', executor: 'agent', agent: 'analyst' },
      { id: 'review', title: 'Ревью', function: 'reviewer', result: 'approve', executor: 'human' },
      { id: 'release', title: 'Релиз', function: 'release_manager', result: 'approve', executor: 'human' },
    ],
  },
  stages: [],
  approvals: [],
  stage_owners: { review: { stage_id: 'review', approval_id: 'x', actor_id: 'bob', actor_role: 'reviewer', taken_at: '2026-10-12T09:20:00Z' } },
  results: { analysis: [{ run_id: 'run-1', artifact_path: 'a.md', revision: 1, id: 'r1', actor_id: 'analyst', created_at: '2026-10-12T09:10:00Z', sha256: '1'.repeat(64) }] },
  allowed_actions: [],
  delivery: { status: 'not_requested' },
};

const events: TaskEvent[] = [
  { version: 1, cursor: 1, run_id: 'run-1', sequence: 1, type: 'attempt_started', attempt_id: 'a1', timestamp: '2026-10-12T09:00:00Z', data: { agent: 'analysis' } },
  { version: 1, cursor: 2, run_id: 'run-1', sequence: 2, type: 'attempt_finished', attempt_id: 'a1', timestamp: '2026-10-12T09:10:00Z', data: { stage: 'analysis', executor: 'agent', outcome: 'passed' } },
  { version: 1, cursor: 3, run_id: 'run-1', sequence: 3, type: 'check_overridden', stage: 'analysis', actor: 'carol', timestamp: '2026-10-12T09:11:00Z', data: { reason: 'flaky' } },
  { version: 1, cursor: 4, run_id: 'run-1', sequence: 4, type: 'transition_selected', attempt_id: 'a1', timestamp: '2026-10-12T09:12:00Z', data: { from: 'analysis', action: 'approve' } },
  { version: 1, cursor: 5, run_id: 'run-1', sequence: 5, type: 'stage_taken', stage: 'review', actor: 'bob', timestamp: '2026-10-12T09:20:00Z', data: {} },
  { version: 1, cursor: 6, run_id: 'run-1', sequence: 6, type: 'run_finished', timestamp: '2026-10-12T10:00:00Z', data: { status: 'completed' } },
];

const metrics = {
  by_stage: [{ stage: 'analysis', tokens_input: 1200, tokens_output: 300, subscription_share_estimate: 1.5, subscription_currency: 'USD' }],
} as unknown as FlowMetrics;

function renderTimeline() {
  return render(<TaskTimeline details={details} events={events} eventTitle={(type) => `title:${type}`} />);
}

beforeEach(async () => {
  const api = await import('../api');
  vi.mocked(api.getTaskFlowMetrics).mockReset().mockResolvedValue(metrics);
});

afterEach(cleanup);

describe('TaskTimeline', () => {
  it('renders lanes with text summaries and no bar for a stage that has not started', async () => {
    renderTimeline();
    const lanes = screen.getByRole('list', { name: 'Этапы задачи на шкале времени' });
    expect(within(lanes).getByRole('img', { name: /Анализ: работа 10 мин, проверка 2 мин, обход проверки/ })).toBeInTheDocument();
    expect(within(lanes).getByRole('img', { name: 'Релиз: этап не начат' })).toBeInTheDocument();
    expect(within(lanes).getByRole('img', { name: /Ревью: работа/ })).toBeInTheDocument();
    expect(screen.getByText('не начат')).toBeInTheDocument();
    await waitFor(async () => expect((await import('../api')).getTaskFlowMetrics).toHaveBeenCalledWith('run-1'));
  });

  it('offers a table alternative with states, executors and marks', () => {
    renderTimeline();
    const table = screen.getByRole('table', { name: 'Интервалы и отметки по этапам' });
    expect(within(table).getAllByText('Работа').length).toBeGreaterThan(0);
    expect(within(table).getAllByText('агент').length).toBeGreaterThan(0);
    expect(within(table).getByText(/Обход проверки .* carol · причина: flaky/)).toBeInTheDocument();
    expect(within(table).getByText('Не начат')).toBeInTheDocument();
  });

  it('opens the stage card with executor, versions, tokens, cost and event links', async () => {
    renderTimeline();
    const button = screen.getByRole('button', { name: /Анализ/ });
    fireEvent.click(button);
    expect(button).toHaveAttribute('aria-expanded', 'true');
    const card = screen.getByRole('region', { name: 'Этап «Анализ»' });
    expect(within(card).getByText('агент analyst')).toBeInTheDocument();
    expect(within(card).getByText('v1 (analyst)')).toBeInTheDocument();
    expect(await within(card).findByText('1 200 / 300')).toBeInTheDocument();
    expect(within(card).getByText(/≈ .*1,50.*приблизительная доля подписки/)).toBeInTheDocument();
    expect(within(card).getByRole('link', { name: 'title:check_overridden' })).toHaveAttribute('href', '#task-event-3');
    expect(within(card).getByRole('link', { name: /лог a1/ })).toHaveAttribute('href', '/api/runs/run-1/logs/a1');
  });

  it('shows unknown usage as no data, not zero', async () => {
    const api = await import('../api');
    vi.mocked(api.getTaskFlowMetrics).mockRejectedValueOnce(new Error('down'));
    renderTimeline();
    fireEvent.click(screen.getByRole('button', { name: /Ревью/ }));
    const card = screen.getByRole('region', { name: 'Этап «Ревью»' });
    expect(await within(card).findAllByText('нет данных (метрики недоступны)')).toHaveLength(2);
    expect(within(card).getByText('bob')).toBeInTheDocument();
  });

  it('supports keyboard navigation between lanes and closing the card with Escape', () => {
    renderTimeline();
    const analysis = screen.getByRole('button', { name: /Анализ/ });
    const review = screen.getByRole('button', { name: /Ревью/ });
    const release = screen.getByRole('button', { name: /Релиз/ });
    analysis.focus();
    fireEvent.keyDown(analysis, { key: 'ArrowDown' });
    expect(review).toHaveFocus();
    fireEvent.keyDown(review, { key: 'End' });
    expect(release).toHaveFocus();
    fireEvent.keyDown(release, { key: 'ArrowDown' });
    expect(analysis).toHaveFocus();

    fireEvent.click(release);
    expect(screen.getByRole('region', { name: 'Этап «Релиз»' })).toHaveTextContent('Этап не начат');
    fireEvent.keyDown(screen.getByRole('button', { name: 'Закрыть карточку' }), { key: 'Escape' });
    expect(screen.queryByRole('region', { name: 'Этап «Релиз»' })).not.toBeInTheDocument();
    expect(release).toHaveFocus();
  });
});
