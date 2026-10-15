import { describe, expect, it } from 'vitest';
import { actionVerbs, allowedActionLabels, allowedActionText } from './actionLabels';

describe('action vocabulary', () => {
  it('uses one label per action: no two verbs share a name', () => {
    const labels = Object.values(actionVerbs);
    expect(new Set(labels).size).toBe(labels.length);
  });

  it('names API actions with the same words as the buttons', () => {
    expect(allowedActionLabels.results).toBe(actionVerbs.submitResult);
    expect(allowedActionLabels['run-agent']).toBe(actionVerbs.runAgent);
    expect(allowedActionLabels.override).toBe(actionVerbs.overrideCheck);
    expect(allowedActionLabels.skip).toBe(actionVerbs.skip);
    expect(allowedActionLabels['plan/approve']).toBe(actionVerbs.acceptPlan);
    expect(allowedActionLabels.confirm).toBe(actionVerbs.accept);
  });

  it('falls back to the API name for an unknown action', () => {
    expect(allowedActionText('something-new')).toBe('something-new');
  });
});
