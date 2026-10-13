import { afterEach, describe, expect, it } from 'vitest';
import { loadStageResultDraft, saveStageResultDraft, stageResultDraftKey } from './stageResultDraft';

class FlakyStorage implements Storage {
  private values = new Map<string, string>();
  failWrites = false;
  failRemoves = false;
  get length() { return this.values.size; }
  clear() { this.values.clear(); }
  getItem(key: string) { return this.values.get(key) ?? null; }
  key(index: number) { return [...this.values.keys()][index] ?? null; }
  removeItem(key: string) {
    if (this.failRemoves) throw new DOMException('blocked', 'SecurityError');
    this.values.delete(key);
  }
  setItem(key: string, value: string) {
    if (this.failWrites) throw new DOMException('quota exceeded', 'QuotaExceededError');
    this.values.set(key, value);
  }
}

function install(storage: Storage) {
  Object.defineProperty(window, 'localStorage', { configurable: true, get: () => storage });
}

const older = { content: 'старый', url: '', note: '', description: '' };
const newer = { content: 'новый', url: '', note: '', description: '' };

afterEach(() => install(new FlakyStorage()));

describe('saveStageResultDraft', () => {
  it('removes the stored older draft when writing the newer one fails', () => {
    const storage = new FlakyStorage();
    install(storage);
    expect(saveStageResultDraft('run', 'stage', older)).toBe(true);
    storage.failWrites = true;

    expect(saveStageResultDraft('run', 'stage', newer)).toBe(false);
    expect(storage.getItem(stageResultDraftKey('run', 'stage'))).toBeNull();
    expect(loadStageResultDraft('run', 'stage')).toBeNull();
  });

  it('reports failure without throwing when the cleanup cannot remove either', () => {
    const storage = new FlakyStorage();
    install(storage);
    saveStageResultDraft('run', 'stage', older);
    storage.failWrites = true;
    storage.failRemoves = true;

    expect(saveStageResultDraft('run', 'stage', newer)).toBe(false);
  });
});
