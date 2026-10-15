import { afterEach, describe, expect, it, vi } from 'vitest';
import { confirmTaskStage, decideApproval, downloadFlowMetricsCsv, getActivePrincipal, getAuthConfig, getFlowMetrics, getGlobalEvents, getTaskFlowMetrics, getPreflight, getRunLog, getRunWorkflow, isAuthenticationRequired, openSession, startRun } from './api';
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

    await startRun(1, 'feature', 'задача');
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

describe('stage confirmation', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('отправляет хеш предмета решения, который видел человек', async () => {
    // The CSRF token is cached by the module, so the calls are told apart by URL.
    const fetchMock = vi.fn((url: string, _init?: RequestInit) => Promise.resolve(new Response(
      url.endsWith('/csrf') || url.includes('session') ? JSON.stringify({ csrf_token: 'csrf-1' }) : '{}', { status: 200 })));
    vi.stubGlobal('fetch', fetchMock);

    await confirmTaskStage('run-1', 'spec', 'c'.repeat(64));
    await confirmTaskStage('run-1', 'spec');

    const confirms = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/stages/spec/confirm'));
    expect(confirms).toHaveLength(2);
    expect(JSON.parse(String((confirms[0][1] as RequestInit).body))).toEqual({ subject_hash: 'c'.repeat(64) });
    expect((confirms[1][1] as RequestInit).body).toBeUndefined();
  });
});

describe('cloud authentication client', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('getAuthConfig запоминает облачный режим для isAuthenticationRequired', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ authentication_required: true, team_management_enabled: false }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ authentication_required: false, team_management_enabled: false }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);

    await getAuthConfig();
    expect(isAuthenticationRequired()).toBe(true);
    await getAuthConfig();
    expect(isAuthenticationRequired()).toBe(false);
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

  it('восстанавливает CSRF через cookie после перезагрузки без Bearer', async () => {
    vi.resetModules();
    const api = await import('./api');
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({
        csrf_token: 'recovered-csrf',
        principal: { actor_id: 'product-1', roles: ['product_owner'] },
      }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ run_id: 'run-after-reload' }), { status: 202 }));
    vi.stubGlobal('fetch', fetchMock);

    await api.openSession();
    await api.startRun(1, 'feature', 'task');

    expect(fetchMock.mock.calls[0][0]).toBe('/api/session');
    expect((fetchMock.mock.calls[0][1] as RequestInit).credentials).toBe('same-origin');
    expect((fetchMock.mock.calls[0][1] as RequestInit).headers).toEqual({});
    expect((fetchMock.mock.calls[1][1] as RequestInit).headers).toMatchObject({ 'X-CSRF-Token': 'recovered-csrf' });
  });

  it('clears an expired session after 401 and exposes a relogin event/message', async () => {
    vi.resetModules();
    const api = await import('./api');
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ csrf_token: 'expired-csrf' }), { status: 200 }))
      .mockResolvedValueOnce(new Response('требуется web session', { status: 401 }))
      .mockResolvedValueOnce(new Response('сессия истекла, войдите снова', { status: 401 }));
    vi.stubGlobal('fetch', fetchMock);
    const onExpired = vi.fn();
    window.addEventListener(api.SESSION_EXPIRED_EVENT, onExpired);

    await expect(api.startRun(1, 'feature', 'task')).rejects.toThrow('Сессия истекла. Войдите снова.');
    expect(onExpired).toHaveBeenCalledTimes(1);
    await expect(api.startRun(1, 'feature', 'task')).rejects.toThrow('Сессия истекла. Войдите снова.');
    expect(onExpired).toHaveBeenCalledTimes(2);

    window.removeEventListener(api.SESSION_EXPIRED_EVENT, onExpired);
  });
});

describe('global event feed API', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('pages newest-first before the persisted global cursor with the server-side warning filter', async () => {
    const fetchMock = vi.fn().mockImplementation(async () => new Response(JSON.stringify({ events: [], next_cursor: 23 }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);

    await getGlobalEvents(0, 100);
    await getGlobalEvents(12, 50, true);

    expect(fetchMock.mock.calls[0][0]).toBe('/api/events?order=desc&limit=100');
    expect(fetchMock.mock.calls[1][0]).toBe('/api/events?order=desc&limit=50&before=12&warnings=true');
    expect(fetchMock.mock.calls[1][1]).toMatchObject({ credentials: 'same-origin' });
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

describe('flow metrics API client', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('requests JSON and CSV for the same half-open period', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ lead_time: { count: 0 } }), { status: 200 }))
      .mockResolvedValueOnce(new Response('metric,stage,value\n', { status: 200, headers: { 'Content-Type': 'text/csv' } }));
    vi.stubGlobal('fetch', fetchMock);

    await getFlowMetrics('2026-10-01', '2026-11-01');
    const blob = await downloadFlowMetricsCsv('2026-10-01', '2026-11-01');

    expect(fetchMock.mock.calls[0][0]).toBe('/api/metrics/flow?from=2026-10-01&to=2026-11-01');
    expect(fetchMock.mock.calls[1][0]).toBe('/api/metrics/flow.csv?from=2026-10-01&to=2026-11-01');
    expect(blob.size).toBe('metric,stage,value\n'.length);
  });

  it('requests per-stage aggregates of one task by run_id', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ by_stage: [] }), { status: 200 }));
    vi.stubGlobal('fetch', fetchMock);

    await getTaskFlowMetrics('run/one');

    expect(fetchMock.mock.calls[0][0]).toBe('/api/metrics/flow?run_id=run%2Fone');
  });

  it('surfaces the API error of a rejected CSV export', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(new Response(
      JSON.stringify({ error: 'to must be later than from', code: 'bad_period' }),
      { status: 400, headers: { 'Content-Type': 'application/json' } },
    )));

    await expect(downloadFlowMetricsCsv('2026-10-02', '2026-10-01')).rejects.toThrow('to must be later than from');
  });
});
