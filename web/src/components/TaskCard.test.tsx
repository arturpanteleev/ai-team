import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { TaskCard } from './TaskCard';
import type { TaskRun } from '../types';

vi.mock('../router', () => ({ useNavigate: () => () => {} }));

const titles: Record<string, string> = { spec: 'Спецификация', impl: 'Реализация', review: 'Код-ревью', qa: 'QA' };
const stageTitle = (id: string) => titles[id] ?? id;

const run: TaskRun = {
  id: 1, run_id: 'run-card-1', feature: 'Онбординг', task: 'Идея роста', status: 'running', phase: 'running',
  current_stage: 'impl', current_function: 'developer', executor: 'agent', executor_agent: 'coder', mode: 'manual',
  allowed_actions: [], started_at: '2026-10-12T10:00:00Z', stage_entered_at: '2026-10-12T10:05:00Z', warnings: 0,
};

afterEach(cleanup);

describe('TaskCard stage status', () => {
  it('shows the computed status, who works, the visit and the route', () => {
    render(<TaskCard run={{
      ...run,
      stage_status: {
        schema_version: 1, terminal: false, current_stage: 'impl', status: 'in_progress', readiness: 'engaged', phase: 'working', outcome: 'pending',
        executor: { kind: 'internal_agent', agent: 'coder', name: 'agentcli', source: 'configured' }, visit: 2, visit_count: 2, returned: true,
        route: [
          { stage_id: 'spec', status: 'passed', readiness: 'left', visited: true, visit_count: 1 },
          { stage_id: 'impl', status: 'in_progress', readiness: 'engaged', visited: true, visit_count: 2, current: true },
          { stage_id: 'review', status: 'not_reached', readiness: 'not_reached', visited: true, visit_count: 1 },
          { stage_id: 'qa', status: 'not_reached', readiness: 'not_reached', visited: false, visit_count: 0 },
        ],
      },
    }} stageTitle={stageTitle} />);
    const card = screen.getByRole('link', { name: /Задача/ });
    expect(within(card).getByText(/Реализация/, { selector: 'dd' })).toBeInTheDocument();
    expect(within(card).getByText('В работе')).toBeInTheDocument();
    expect(within(card).getByText('Внутренний агент: coder (agentcli)')).toBeInTheDocument();
    expect(within(card).getByText('2 из 2 · возврат')).toBeInTheDocument();
    const route = within(card).getByRole('list', { name: 'Статус каждого этапа маршрута' });
    const items = within(route).getAllByRole('listitem');
    expect(items).toHaveLength(4);
    expect(items[1]).toHaveAttribute('data-current', 'true');
    expect(items[2]).toHaveAttribute('data-visited', 'true');
    expect(items[2]).toHaveAttribute('data-status', 'not_reached');
    expect(items[2]).toHaveTextContent('Код-ревью: Не достигнут');
    expect(items[1]).toHaveTextContent('Реализация: В работе, посещений: 2');
    expect(items[3]).toHaveTextContent('QA: Не достигнут');
  });

  it('tells a person waiting apart from checking and shows what blocks the stage', () => {
    render(<TaskCard run={{
      ...run, status: 'waiting_for_approval', current_stage: 'review',
      stage_status: {
        schema_version: 1, terminal: false, current_stage: 'review', status: 'awaiting_human', readiness: 'engaged', phase: 'awaiting_human', outcome: 'rejected',
        executor: { kind: 'external_agent', name: 'container-review', source: 'attempt' }, visit: 1, visit_count: 1,
        blocked_by: { kind: 'approval', roles: ['reviewer'], actions: ['approve', 'return_to_impl'] },
        route: [{ stage_id: 'review', status: 'awaiting_human', readiness: 'engaged', visited: true, visit_count: 1, current: true }],
      },
    }} stageTitle={stageTitle} />);
    expect(screen.getByText('Ждёт человека')).toBeInTheDocument();
    expect(screen.getByText('Внешний агент: container-review')).toBeInTheDocument();
    expect(screen.getByText('нужно подтверждение результата')).toBeInTheDocument();
    expect(screen.queryByText('Посещение')).not.toBeInTheDocument();
  });

  it('falls back to the plain stage and executor when the server sent no projection', () => {
    render(<TaskCard run={run} stageTitle={stageTitle} />);
    expect(screen.getByText('Агент: coder')).toBeInTheDocument();
    expect(screen.queryByRole('list', { name: 'Статус каждого этапа маршрута' })).not.toBeInTheDocument();
  });

  it('shows only the route for a finished task', () => {
    render(<TaskCard run={{
      ...run, status: 'completed', current_stage: undefined,
      stage_status: { schema_version: 1, terminal: true, route: [{ stage_id: 'spec', status: 'passed', readiness: 'left', visited: true, visit_count: 1 }] },
    }} stageTitle={stageTitle} />);
    expect(screen.getByRole('list', { name: 'Статус каждого этапа маршрута' })).toBeInTheDocument();
    expect(screen.queryByText('Исполнитель')).not.toBeInTheDocument();
  });
});
