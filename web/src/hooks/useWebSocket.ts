import { useEffect, useRef } from 'react';
import type { WsEvent } from '../types';

const cursorStorageKey = 'ai-team:event-cursor';

interface UseWebSocketOptions {
  onEvent?: (event: WsEvent) => void;
}

export function useWebSocket({ onEvent }: UseWebSocketOptions) {
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectTimeout = useRef<number>(0);
  const reconnectDelay = useRef(1000);
  const cursorRef = useRef(0);
  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;

  useEffect(() => {
    let alive = true;
    const savedCursor = Number(window.sessionStorage.getItem(cursorStorageKey));
    cursorRef.current = Number.isSafeInteger(savedCursor) && savedCursor >= 0 ? savedCursor : 0;

    function connect() {
      if (!alive) return;

      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const host = window.location.host;
      const ws = new WebSocket(`${protocol}//${host}/ws?cursor=${cursorRef.current}`);

      ws.onopen = () => {
        reconnectDelay.current = 1000;
      };

      ws.onmessage = (event) => {
        try {
          const data: WsEvent = JSON.parse(event.data);
          if (data.version !== 1 || !Number.isSafeInteger(data.cursor) || data.cursor <= cursorRef.current) {
            return;
          }
          cursorRef.current = data.cursor;
          window.sessionStorage.setItem(cursorStorageKey, String(data.cursor));
          onEventRef.current?.(data);
        } catch {
          // ignore
        }
      };

      ws.onclose = () => {
        if (!alive) return;
        reconnectTimeout.current = window.setTimeout(() => {
          reconnectDelay.current = Math.min(reconnectDelay.current * 2, 30000);
          connect();
        }, reconnectDelay.current);
      };

      ws.onerror = () => {
        ws.close();
      };

      wsRef.current = ws;
    }

    connect();

    return () => {
      alive = false;
      if (reconnectTimeout.current) {
        clearTimeout(reconnectTimeout.current);
      }
      wsRef.current?.close();
    };
  }, []);
}
