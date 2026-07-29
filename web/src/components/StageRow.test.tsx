import { fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from '../router';
import { describe, expect, it } from 'vitest';
import { StageRow } from './StageRow';
import type { Artifact, Stage } from '../types';

describe('StageRow', () => {
  it('links to immutable run evidence and exposes failure details', () => {
    const stage: Stage = {
      id: 1,
      pipeline_run_id: 10,
      attempt_id: '002-reviewer',
      stage_index: 2,
      agent_name: 'reviewer',
      status: 'failed',
      started_at: '2026-07-20T00:00:00Z',
      error: 'required check failed',
      execution: 'succeeded',
      decision: 'rejected',
      outcome: 'failed',
      checks_json: JSON.stringify([{
        name: 'go-test', class: 'unit', command: ['go', 'test', './...'],
        policy: 'required', status: 'failed', exit_code: 1,
      }]),
      mutations_json: JSON.stringify(['reverse.go']),
      delivery_json: JSON.stringify({
        pr_url: 'https://example.test/pr/1',
        steps: [{ step: 'push', status: 'passed', exit_code: 0 }],
      }),
    };
    const artifacts: Artifact[] = [{
      name: 'review.md',
      path: 'attempts/002-reviewer/artifacts/feat/review.md',
      run_id: 'run-immutable',
      size: 42,
      mod_time: '2026-07-20T00:00:01Z',
    }];
    render(<MemoryRouter><StageRow stage={stage} artifacts={artifacts} /></MemoryRouter>);
    fireEvent.click(screen.getByText('reviewer'));
    expect(screen.getByRole('link', { name: 'review.md' })).toHaveAttribute(
      'href',
      '/artifacts/run-immutable/attempts/002-reviewer/artifacts/feat/review.md',
    );
    expect(screen.getByText('required check failed')).toBeInTheDocument();
    expect(screen.getByText('002-reviewer')).toBeInTheDocument();
    expect(screen.getByText('rejected')).toBeInTheDocument();
    expect(screen.getByText('go-test')).toBeInTheDocument();
    expect(screen.getByText('reverse.go')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'https://example.test/pr/1' })).toBeInTheDocument();
  });
});
