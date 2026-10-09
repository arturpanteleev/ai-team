import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useWebSocket } from './useWebSocket';
import type { WsEvent } from '../types';

class MockWebSocket {
  static instances: MockWebSocket[] = [];
  url: string;
  onopen: (() => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
  }

  close() {
    this.onclose?.();
  }

  message(event: WsEvent) {
    this.onmessage?.({ data: JSON.stringify(event) } as MessageEvent);
  }
}

describe('useWebSocket', () => {
  beforeEach(() => {
    MockWebSocket.instances = [];
    window.sessionStorage.clear();
    vi.stubGlobal('WebSocket', MockWebSocket);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('восстанавливает cursor и игнорирует дубликаты', () => {
    window.sessionStorage.setItem(
      'ai-team:event-cursor',
      JSON.stringify({ stream: 's1', cursor: 4 }),
    );
    const onEvent = vi.fn();
    const { unmount } = renderHook(() => useWebSocket({ onEvent }));
    const socket = MockWebSocket.instances[0];
    expect(socket.url).toContain('/ws?cursor=4');

    const event: WsEvent = {
      version: 1,
      stream: 's1',
      cursor: 5,
      run_id: 'run-1',
      sequence: 1,
      type: 'run_started',
      timestamp: new Date().toISOString(),
      data: { feature: 'demo' },
    };
    act(() => {
      socket.message(event);
      socket.message(event);
    });

    expect(onEvent).toHaveBeenCalledTimes(1);
    expect(window.sessionStorage.getItem('ai-team:event-cursor')).toBe(
      JSON.stringify({ stream: 's1', cursor: 5 }),
    );
    unmount();
  });

  it('сбрасывает cursor при смене stream identity', () => {
    window.sessionStorage.setItem(
      'ai-team:event-cursor',
      JSON.stringify({ stream: 'old', cursor: 100 }),
    );
    const onEvent = vi.fn();
    const { unmount } = renderHook(() => useWebSocket({ onEvent }));
    const socket = MockWebSocket.instances[0];
    expect(socket.url).toContain('/ws?cursor=100');

    const event: WsEvent = {
      version: 1,
      stream: 'new',
      cursor: 3,
      run_id: 'run-1',
      sequence: 1,
      type: 'run_started',
      timestamp: new Date().toISOString(),
      data: { feature: 'demo' },
    };
    act(() => {
      socket.message(event);
    });

    expect(onEvent).toHaveBeenCalledTimes(1);
    expect(window.sessionStorage.getItem('ai-team:event-cursor')).toBe(
      JSON.stringify({ stream: 'new', cursor: 3 }),
    );
    unmount();
  });

  it('переподключается с сохранённым cursor после возврата соединения', () => {
    vi.useFakeTimers();
    window.sessionStorage.setItem(
      'ai-team:event-cursor',
      JSON.stringify({ stream: 's1', cursor: 8 }),
    );
    const { unmount } = renderHook(() => useWebSocket({ onEvent: vi.fn() }));
    expect(MockWebSocket.instances[0].url).toContain('/ws?cursor=8');

    act(() => {
      MockWebSocket.instances[0].close();
      vi.advanceTimersByTime(1000);
    });

    expect(MockWebSocket.instances).toHaveLength(2);
    expect(MockWebSocket.instances[1].url).toContain('/ws?cursor=8');
    unmount();
    vi.useRealTimers();
  });
});
