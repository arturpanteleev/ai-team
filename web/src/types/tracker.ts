// Трекер задач (S14/S15): ответы /api/tracker*, /api/runs/{id}/tracker.

export interface TrackerLink {
  task_id: number;
  tracker: string;
  issue_key: string;
  linked_by?: string;
  linked_at: string;
}

export interface TrackerLinkConflict {
  task_id: number;
  issue_key: string;
  reason: string;
  detected_at: string;
}

export interface TrackerDelivery {
  event_id: number;
  task_id: number;
  tracker: string;
  issue_key: string;
  event_type: string;
  stage?: string;
  status: 'delivered' | 'failed' | 'abandoned' | 'superseded' | string;
  attempts: number;
  last_error?: string;
  transition_to?: string;
  note?: string;
  created_at: string;
  updated_at: string;
  delivered_at?: string;
}

export type TrackerInboundState = 'received' | 'applied' | 'pending' | 'conflict' | 'ignored' | 'superseded' | 'dismissed';

export interface TrackerInbound {
  tracker: string;
  change_id: string;
  task_id: number;
  issue_key: string;
  from_status: string;
  to_status: string;
  origin: string;
  author?: string;
  changed_at: string;
  state: TrackerInboundState;
  action?: string;
  stage?: string;
  to_stage?: string;
  reason?: string;
  suggested_action?: string;
  resolved_by?: string;
  detected_at: string;
  updated_at: string;
  // Действия, которые контроллер разрешает по задаче прямо сейчас (для открытого запроса).
  allowed_actions?: string[];
}

export type TrackerSyncStatus = 'not_configured' | 'not_linked' | 'conflict' | 'failed' | 'inbound_pending' | 'ok';

export interface RunTracker {
  configured: boolean;
  tracker?: string;
  link?: TrackerLink | null;
  conflicts?: TrackerLinkConflict[] | null;
  deliveries?: TrackerDelivery[] | null;
  inbound?: TrackerInbound[] | null;
  sync_status: TrackerSyncStatus;
}

export interface TrackerPollState {
  tracker: string;
  cursor: string;
  last_poll_at?: string;
  last_success_at?: string;
  last_error?: string;
  last_error_at?: string;
  consecutive_failures: number;
  last_changes: number;
}

export interface TrackerInboundSummary {
  configured: boolean;
  poll_interval_seconds?: number;
  pending_requests?: number;
  conflicts?: number;
  status?: 'never_polled' | 'ok' | 'failed';
  delay_seconds?: number;
  sync?: TrackerPollState;
  rules?: { status: string; action: string; stage?: string; to_stage?: string }[];
  tracker?: string;
}

export interface TrackerOverview {
  configured: boolean;
  tracker?: string;
  failed_deliveries?: number;
  link_conflicts?: number;
  inbound?: TrackerInboundSummary;
}

export interface TrackerSyncResult {
  tracker: string;
  trigger: string;
  changes: number;
  applied: number;
  pending: number;
  conflicts: number;
  ignored: number;
  duplicates: number;
  cursor: string;
  coalesced?: boolean;
}
