import { describe, expect, it } from 'vitest';
import { hasHttpPrefix, linkError } from './stageResultValidation';

describe('linkError', () => {
  it('requires the literal http:// or https:// prefix that new URL() alone does not', () => {
    for (const value of ['https:ci.example/build/7', 'http:/ci.example/build/7', 'https:///ci.example', 'ci.example/build']) {
      expect(linkError(value), value).toBe('Введите полный адрес, начиная с https://.');
    }
  });

  it('names the allowed schemes for another scheme', () => {
    expect(linkError('ftp://ci.example/build/7')).toBe('Ссылка должна начинаться с http:// или https://.');
  });

  it('accepts full http and https links regardless of scheme case', () => {
    for (const value of ['https://ci.example/build/7', 'http://ci.example', ' HTTPS://ci.example/x ']) {
      expect(linkError(value), value).toBe('');
    }
  });

  it('still rejects credentials inside a well-formed link', () => {
    expect(linkError('https://bot:secret@ci.example/build/7')).toBe('Уберите логин и пароль из ссылки.');
  });
});

describe('hasHttpPrefix', () => {
  it('needs a host right after the scheme', () => {
    expect(hasHttpPrefix('https://tracker.example/JIRA-1')).toBe(true);
    expect(hasHttpPrefix('https:tracker.example/JIRA-1')).toBe(false);
    expect(hasHttpPrefix('https://')).toBe(false);
    expect(hasHttpPrefix('JIRA-1')).toBe(false);
  });
});
