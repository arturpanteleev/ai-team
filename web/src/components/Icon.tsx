import type { ReactNode } from 'react';

// Один набор значков для статусов, исполнителей и предупреждений: контур 1.8px,
// цвет наследуется от текста, значок всегда сопровождается подписью рядом.
export type IconName =
  | 'not_reached' | 'ready' | 'in_progress' | 'checking' | 'awaiting_human' | 'passed' | 'rejected' | 'failed'
  | 'blocked' | 'skipped' | 'canceled' | 'human' | 'agent' | 'external' | 'warning' | 'override' | 'tracker' | 'log';

const paths: Record<IconName, ReactNode> = {
  not_reached: <circle cx="12" cy="12" r="6.5" strokeDasharray="2.5 3" />,
  ready: <><circle cx="12" cy="12" r="7" /><path d="M12 8.5v3.5l2.5 1.5" /></>,
  in_progress: <path d="M8 6.5l10 5.5-10 5.5z" />,
  checking: <><path d="M6 12a6 6 0 0 1 10.2-4.3L18 9.5" /><path d="M18 5.5v4h-4" /><path d="M18 12a6 6 0 0 1-10.2 4.3L6 14.5" /><path d="M6 18.5v-4h4" /></>,
  awaiting_human: <><circle cx="12" cy="8.5" r="3" /><path d="M6 19c.7-3.2 3-4.8 6-4.8s5.3 1.6 6 4.8" /></>,
  passed: <path d="M6 12.5l4 4 8-9" />,
  rejected: <path d="M7 7l10 10M17 7L7 17" />,
  failed: <path d="M12 6.5v7M12 17.5h.01" />,
  blocked: <><circle cx="12" cy="12" r="7" /><path d="M7.2 7.2l9.6 9.6" /></>,
  skipped: <path d="M6 7l5 5-5 5M12 7l5 5-5 5" />,
  canceled: <path d="M7 12h10" />,
  human: <><circle cx="12" cy="8.5" r="3" /><path d="M6 19c.7-3.2 3-4.8 6-4.8s5.3 1.6 6 4.8" /></>,
  agent: <><rect x="5" y="8" width="14" height="10" rx="3" /><path d="M12 8V5M9.5 12.5h.01M14.5 12.5h.01M9.5 15.5h5" /></>,
  external: <><rect x="4.5" y="9" width="11" height="10" rx="2" /><path d="M10 5h9v9M19 5l-7 7" /></>,
  warning: <><path d="M12 5l8 14H4z" /><path d="M12 10.5v4M12 17h.01" /></>,
  override: <><path d="M12 5l8 14H4z" /><path d="M9.5 12.5l5 0" /></>,
  tracker: <><path d="M9.5 14.5l5-5" /><path d="M11 7.5l1-1a3.5 3.5 0 0 1 5 5l-1 1" /><path d="M13 16.5l-1 1a3.5 3.5 0 0 1-5-5l1-1" /></>,
  log: <><path d="M7 4.5h8l3 3V19a.5.5 0 0 1-.5.5h-10.5A.5.5 0 0 1 7 19z" /><path d="M9.5 12h5M9.5 15h5" /></>,
};

export function Icon({ name, size = 14, className }: { name: IconName; size?: number; className?: string }) {
  return (
    <svg viewBox="0 0 24 24" width={size} height={size} fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false" className={className}>
      {paths[name]}
    </svg>
  );
}
