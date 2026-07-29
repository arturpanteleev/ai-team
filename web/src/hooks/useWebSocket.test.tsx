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
    window.sessionStorage.setItem('ai-team:event-cursor', '4');
    const onEvent = vi.fn();
    const { unmount } = renderHook(() => useWebSocket({ onEvent }));
    const socket = MockWebSocket.instances[0];
    expect(socket.url).toContain('/ws?cursor=4');

    const event: WsEvent = {
      version: 1,
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
    expect(window.sessionStorage.getItem('ai-team:event-cursor')).toBe('5');
    unmount();
  });
});
