import { describe, expect, it } from 'vitest';
import { classifyIssue } from './taskIssue';

describe('classifyIssue', () => {
  it('treats a provenance drift as a version choice the user can accept', () => {
    expect(classifyIssue('resume run: provenance drift: runtime (глобальный) изменился').code).toBe('version');
  });

  it('explains a failed Claude login without repeating the raw log', () => {
    const issue = classifyIssue('агент architect: claude-authentication: аутентификация claude недоступна');
    expect(issue.code).toBe('auth');
    expect(issue.explanation).not.toContain('claude-authentication');
  });

  it('recognizes a plain agent failure', () => {
    expect(classifyIssue('агент architect: claude завершился с ошибкой; причина: exit status 1').code).toBe('agent');
  });

  it('falls back to a generic stop for unknown text', () => {
    expect(classifyIssue('Сборка упала на шаге проверки').code).toBe('other');
  });
});
