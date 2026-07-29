import { afterEach, describe, expect, it, vi } from 'vitest';
import { decideApproval, getActivePrincipal, getPreflight, getRunLog, getRunWorkflow, openSession, startRun } from './api';
import type { Approval } from './types';

describe('write API client', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('получает session, отправляет CSRF и сохраняет exact subject', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ csrf_token: 'csrf-1' }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ run_id: 'run-1' }), { status: 202 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: 'approval-1', status: 'resolved' }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);

    await startRun('feature', 'задача');
    const approval = {
      id: 'approval-1',
      run_id: 'run-1',
      attempt_id: 'attempt-1',
      from_stage: 'analyst',
      to_stage: 'architect',
      trigger: 'stage_completed',
      subject_hash: 'a'.repeat(64),
      required_roles: ['product_owner'],
      quorum: 'any',
      actions: ['approve'],
      status: 'pending',
      created_at: new Date().toISOString(),
    } satisfies Approval;
    await decideApproval('run-1', approval, {
      actor_id: 'product-1', actor_role: 'product_owner', action: 'approve',
    });

    expect(fetchMock).toHaveBeenCalledTimes(3);
    const startOptions = fetchMock.mock.calls[1][1] as RequestInit;
    expect((startOptions.headers as Record<string, string>)['X-CSRF-Token']).toBe('csrf-1');
    const decisionOptions = fetchMock.mock.calls[2][1] as RequestInit;
    expect(JSON.parse(String(decisionOptions.body))).toMatchObject({
      actor_id: 'product-1',
      subject_hash: 'a'.repeat(64),
    });
  });
});

describe('cloud authentication client', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('передаёт Bearer только при login и сохраняет trusted principal', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({
      csrf_token: 'csrf-cloud',
      principal: { actor_id: 'reviewer-1', roles: ['reviewer'] },
    }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);

    await openSession('signed-token');

    expect((fetchMock.mock.calls[0][1] as RequestInit).headers).toMatchObject({
      Authorization: 'Bearer signed-token',
    });
    expect(getActivePrincipal()).toEqual({ actor_id: 'reviewer-1', roles: ['reviewer'] });
  });
});

describe('observability API client', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('читает preflight и кодирует exact log identities', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ ready: true, checks: [] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({
        run_id: 'run/one', attempt_id: 'attempt one', offset: 0, truncated: false, content: 'ok',
      }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ schema_version: 2 }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);

    await getPreflight();
    await getRunLog('run/one', 'attempt one');
    await getRunWorkflow('run/one');

    expect(fetchMock.mock.calls[0][0]).toBe('/api/preflight');
    expect(fetchMock.mock.calls[1][0]).toBe('/api/runs/run%2Fone/logs/attempt%20one');
    expect(fetchMock.mock.calls[2][0]).toBe('/api/runs/run%2Fone/workflow');
  });
});
