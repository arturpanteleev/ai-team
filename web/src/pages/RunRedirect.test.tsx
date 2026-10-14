import { render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { getPipelineRun } from '../api';
import { MemoryRouter, Routes, useParams } from '../router';
import { PipelineRedirect, RunRedirect } from './RunRedirect';

vi.mock('../api', () => ({
  getPipelineRun: vi.fn().mockResolvedValue({ run: { id: 42, run_id: 'run-42' }, stages: [] }),
}));

function Destination() {
  const { runID = '' } = useParams<{ runID: string }>();
  return <span>Task {runID}</span>;
}

describe('старые адреса задачи ведут на /tasks/:id', () => {
  it('/runs/:runID сразу открывает единую страницу задачи', async () => {
    render(<MemoryRouter initialEntries={['/runs/run-42']}>
      <Routes routes={[
        { path: '/runs/:runID', element: <RunRedirect /> },
        { path: '/tasks/:runID', element: <Destination /> },
      ]} />
    </MemoryRouter>);

    expect(await screen.findByText('Task run-42')).toBeInTheDocument();
  });

  it('/pipelines/:id переводит числовой id в run_id и открывает /tasks/:id', async () => {
    render(<MemoryRouter initialEntries={['/pipelines/42']}>
      <Routes routes={[
        { path: '/pipelines/:id', element: <PipelineRedirect /> },
        { path: '/tasks/:runID', element: <Destination /> },
      ]} />
    </MemoryRouter>);

    expect(await screen.findByText('Task run-42')).toBeInTheDocument();
    await waitFor(() => expect(getPipelineRun).toHaveBeenCalledWith(42));
  });
});
