import { useEffect, useRef } from 'react';
import type { WsEvent } from '../types';

interface StoredCursor {
  stream?: string;
  cursor: number;
}

const cursorStorageKey = 'ai-team:event-cursor';

function loadStoredCursor(): StoredCursor {
  try {
    const raw = window.sessionStorage.getItem(cursorStorageKey);
    if (raw) {
      const parsed = JSON.parse(raw) as StoredCursor;
      if (Number.isSafeInteger(parsed.cursor) && parsed.cursor >= 0) {
        return parsed;
      }
    }
  } catch {
    // ignore
  }
  return { cursor: 0 };
}

function saveStoredCursor(value: StoredCursor) {
  window.sessionStorage.setItem(cursorStorageKey, JSON.stringify(value));
}

interface UseWebSocketOptions {
  onEvent?: (event: WsEvent) => void;
}

export function useWebSocket({ onEvent }: UseWebSocketOptions) {
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectTimeout = useRef<number>(0);
  const reconnectDelay = useRef(1000);
  const cursorRef = useRef<StoredCursor>({ cursor: 0 });
  const onEventRef = useRef(onEvent);
  onEventRef.current = onEvent;

  useEffect(() => {
    let alive = true;
    cursorRef.current = loadStoredCursor();

    function connect() {
      if (!alive) return;

      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const host = window.location.host;
      const ws = new WebSocket(`${protocol}//${host}/ws?cursor=${cursorRef.current.cursor}`);

      ws.onopen = () => {
        reconnectDelay.current = 1000;
      };

      ws.onmessage = (event) => {
        try {
          const data: WsEvent = JSON.parse(event.data);
          if (data.version !== 1 || !Number.isSafeInteger(data.cursor)) {
            return;
          }
          // Смена stream identity означает пересоздание event store:
          // сохранённый cursor относится к другой последовательности.
          const streamChanged =
            data.stream !== undefined && data.stream !== cursorRef.current.stream;
          if (!streamChanged && data.cursor <= cursorRef.current.cursor) {
            return;
          }
          cursorRef.current = { stream: data.stream, cursor: data.cursor };
          saveStoredCursor(cursorRef.current);
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
