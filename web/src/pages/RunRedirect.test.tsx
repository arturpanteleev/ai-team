import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { getTaskRun } from '../api';
import { MemoryRouter, Routes, useParams } from '../router';
import { RunRedirect } from './RunRedirect';

vi.mock('../api', () => ({
  getTaskRun: vi.fn().mockResolvedValue({ run: { id: 42, run_id: 'run-42' }, stages: [] }),
}));

function Destination() {
  const { id } = useParams<{ id: string }>();
  return <span>Task {id}</span>;
}

describe('event task navigation', () => {
  it('resolves the stable run identity to the existing task detail route', async () => {
    render(<MemoryRouter initialEntries={['/runs/run-42']}>
      <Routes routes={[
        { path: '/runs/:runID', element: <RunRedirect /> },
        { path: '/pipelines/:id', element: <Destination /> },
      ]} />
    </MemoryRouter>);

    expect(await screen.findByText('Task 42')).toBeInTheDocument();
    expect(getTaskRun).toHaveBeenCalledWith('run-42');
  });
});
