// Browser-local drafts for human stage results. Drafts are a convenience for
// the person filling the form: storage may be missing, blocked or full, so every
// access is guarded and the page keeps working with the in-memory value only.

export interface StageResultDraft {
  content: string;
  url: string;
  note: string;
  description: string;
}

const PREFIX = 'ai-team:stage-result-draft:v1';

export const emptyStageResultDraft: StageResultDraft = { content: '', url: '', note: '', description: '' };

export function stageResultDraftKey(runId: string, stageId: string): string {
  return `${PREFIX}:${encodeURIComponent(runId)}:${encodeURIComponent(stageId)}`;
}

function storage(): Storage | null {
  try {
    return typeof window === 'undefined' ? null : window.localStorage;
  } catch {
    return null;
  }
}

export function isEmptyStageResultDraft(draft: StageResultDraft): boolean {
  return !draft.content && !draft.url && !draft.note && !draft.description;
}

export function loadStageResultDraft(runId: string, stageId: string): StageResultDraft | null {
  try {
    const raw = storage()?.getItem(stageResultDraftKey(runId, stageId));
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<Record<keyof StageResultDraft, unknown>>;
    const text = (value: unknown) => (typeof value === 'string' ? value : '');
    const draft = { content: text(parsed.content), url: text(parsed.url), note: text(parsed.note), description: text(parsed.description) };
    return isEmptyStageResultDraft(draft) ? null : draft;
  } catch {
    return null;
  }
}

// saveStageResultDraft reports whether the draft is persisted in the browser.
// An empty draft removes the stored entry. When a write fails (quota, blocked
// storage) the previously stored version is removed too: otherwise a reload
// would silently restore an older draft than the one on screen.
export function saveStageResultDraft(runId: string, stageId: string, draft: StageResultDraft): boolean {
  const store = storage();
  if (!store) return false;
  const key = stageResultDraftKey(runId, stageId);
  try {
    if (isEmptyStageResultDraft(draft)) store.removeItem(key);
    else store.setItem(key, JSON.stringify(draft));
    return true;
  } catch {
    try {
      store.removeItem(key);
    } catch {
      // Nothing more can be done; loadStageResultDraft tolerates any leftover.
    }
    return false;
  }
}

export function clearStageResultDraft(runId: string, stageId: string): void {
  try {
    storage()?.removeItem(stageResultDraftKey(runId, stageId));
  } catch {
    // Storage is optional; a stale draft is harmless and is overwritten next time.
  }
}
