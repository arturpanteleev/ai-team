import { cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { TaskEvent, TaskRunDetails } from '../types';
import { TaskTimeline } from './TaskTimeline';

vi.mock('../api', () => ({ getTaskFlowMetrics: vi.fn() }));

const baseDetails: TaskRunDetails = {
  run: { id: 1, run_id: 'run-1', feature: 'Маршрут', status: 'running', started_at: '2026-10-12T09:00:00Z' },
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
  stage_owners: {},
  results: {},
  allowed_actions: [],
  delivery: { status: 'not_requested' },
};

const events: TaskEvent[] = [
  { version: 1, cursor: 1, run_id: 'run-1', sequence: 1, type: 'attempt_started', attempt_id: 'a1', timestamp: '2026-10-12T09:00:00Z', data: { agent: 'analysis' } },
  { version: 1, cursor: 2, run_id: 'run-1', sequence: 2, type: 'attempt_finished', attempt_id: 'a1', timestamp: '2026-10-12T09:10:00Z', data: { stage: 'analysis', executor: 'agent', outcome: 'passed' } },
];

const details: TaskRunDetails = {
  ...baseDetails,
  stage_statuses: {
    schema_version: 1, terminal: false, current_stage: 'analysis',
    stages: [
      {
        stage_id: 'analysis', index: 1, status: 'in_progress', readiness: 'engaged', phase: 'working', outcome: 'pending',
        executor: { kind: 'internal_agent', agent: 'analyst', name: 'agentcli', source: 'configured' }, current: true, visited: true,
        visit_count: 2, active_visit: 2, allowed_actions: [],
        visits: [
          { number: 1, active: false, entered_at: '2026-10-12T09:00:00Z', entered_via: 'start', left_at: '2026-10-12T09:12:00Z', left_to: 'review',
            outcome: 'passed', executor: { kind: 'internal_agent', agent: 'analyst', source: 'attempt' }, attempts: [] },
          { number: 2, active: true, entered_at: '2026-10-12T09:40:00Z', entered_from: 'review', entered_via: 'return', outcome: 'pending',
            executor: { kind: 'internal_agent', agent: 'analyst', source: 'configured' }, attempts: [] },
        ],
      },
      {
        stage_id: 'review', index: 2, status: 'not_reached', readiness: 'not_reached', phase: 'none', outcome: 'rejected',
        executor: { kind: 'human', source: 'attempt' }, current: false, visited: true, visit_count: 1, active_visit: 0, allowed_actions: [],
        visits: [{ number: 1, active: false, entered_at: '2026-10-12T09:12:00Z', entered_from: 'analysis', entered_via: 'transition',
          left_at: '2026-10-12T09:40:00Z', left_to: 'analysis', returned: true, superseded: true, outcome: 'rejected',
          executor: { kind: 'human', actor: 'bob', source: 'attempt' }, attempts: [] }],
      },
      {
        stage_id: 'release', index: 3, status: 'not_reached', readiness: 'not_reached', phase: 'none', outcome: 'none',
        executor: { kind: 'human', source: 'configured' }, current: false, visited: false, visit_count: 0, active_visit: 0, allowed_actions: [], visits: [],
      },
    ],
  },
};

const clock = () => Date.parse('2026-10-12T10:00:00Z');

beforeEach(async () => {
  const api = await import('../api');
  vi.mocked(api.getTaskFlowMetrics).mockReset().mockResolvedValue({ by_stage: [] } as never);
});

afterEach(cleanup);

describe('TaskTimeline stage statuses and visits (S11)', () => {
  it('shows the same status words on every lane and the visit bands under the scale', () => {
    render(<TaskTimeline details={details} events={events} eventTitle={(type) => type} clock={clock} />);
    const lanes = screen.getByRole('list', { name: 'Этапы задачи на шкале времени' });
    expect(within(lanes).getByText('В работе')).toBeInTheDocument();
    expect(within(lanes).getAllByText('Не достигнут')).toHaveLength(2);
    expect(within(lanes).getByText(/посещений: 2/)).toBeInTheDocument();
    expect(within(lanes).getByText(/бывал активен/)).toBeInTheDocument();
    const analysisVisits = within(lanes).getByRole('list', { name: 'Посещения этапа «Анализ»' });
    const bands = within(analysisVisits).getAllByRole('listitem');
    expect(bands).toHaveLength(2);
    expect(bands[0]).not.toHaveAttribute('data-active');
    expect(bands[1]).toHaveAttribute('data-active', 'true');
    expect(bands[1]).toHaveAttribute('data-returned', 'true');
    expect(bands[0].getAttribute('title')).toContain('неактивно');
    expect(bands[1].getAttribute('title')).toContain('возврат с «Ревью»');
    expect(within(lanes).queryByRole('list', { name: 'Посещения этапа «Релиз»' })).not.toBeInTheDocument();
  });

  it('lists the visits in the stage card', () => {
    render(<TaskTimeline details={details} events={events} eventTitle={(type) => type} clock={clock} />);
    fireEvent.click(screen.getByRole('button', { name: /Анализ/ }));
    const card = screen.getByRole('region', { name: /Этап «Анализ»/ });
    expect(within(card).getByText(/Посещение 1 · неактивно · пройден/)).toBeInTheDocument();
    expect(within(card).getByText(/Посещение 2 · активно · не завершён/)).toBeInTheDocument();
  });

  it('keeps the old lane summary when the server sent no projection', () => {
    render(<TaskTimeline details={baseDetails} events={events} eventTitle={(type) => type} clock={clock} />);
    expect(screen.queryByText('Не достигнут')).not.toBeInTheDocument();
    expect(screen.getAllByText('не начат').length).toBeGreaterThan(0);
  });
});
