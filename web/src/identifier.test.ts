import { describe, expect, it } from 'vitest';
import { identifierError, identifierFromTitle, identifierPattern, isIdentifierServerError } from './identifier';

describe('identifierFromTitle', () => {
  it('transliterates Cyrillic and joins words with hyphens', () => {
    expect(identifierFromTitle('Онбординг для новых клиентов')).toBe('onbording-dlya-novykh-klientov');
    expect(identifierFromTitle('Вход через Google Auth')).toBe('vkhod-cherez-google-auth');
  });

  it('cleans punctuation and trims edges', () => {
    expect(identifierFromTitle('  --Рост: повторных продаж!! ')).toBe('rost-povtornykh-prodazh');
  });

  it('returns a value that passes the server pattern, or empty', () => {
    for (const title of ['рост повторных продаж', 'Ёлка 2.0', '***', 'a'.repeat(100), 'Ъъ Ьь']) {
      const id = identifierFromTitle(title);
      expect(id === '' || identifierPattern.test(id), title).toBe(true);
    }
    expect(identifierFromTitle('***')).toBe('');
  });
});

describe('identifierError', () => {
  it('accepts values matching the server pattern, including a trailing hyphen', () => {
    for (const value of ['google-auth', 'a', '1.2_3-x', 'x-', 'A'.repeat(64)]) {
      expect(identifierError(value), value).toBe('');
    }
  });

  it('explains an empty value', () => {
    expect(identifierError('')).toBe('Укажите идентификатор.');
  });

  it('names the invalid character, including spaces and Cyrillic', () => {
    expect(identifierError('google auth')).toBe('Недопустимый символ « ». Используйте латиницу, цифры, «.», «_» или «-».');
    expect(identifierError('auth-вход')).toBe('Недопустимый символ «в». Используйте латиницу, цифры, «.», «_» или «-».');
  });

  it('requires the first character to be a letter or digit', () => {
    expect(identifierError('-auth')).toBe('Первый символ должен быть латинской буквой или цифрой, а не «-».');
    expect(identifierError('.env')).toBe('Первый символ должен быть латинской буквой или цифрой, а не «.».');
  });

  it('rejects values longer than 64 characters', () => {
    expect(identifierError('a'.repeat(65))).toBe('Слишком длинный идентификатор: 65 символов, максимум 64.');
  });
});

describe('isIdentifierServerError', () => {
  it('routes feature-related server errors to the identifier field', () => {
    expect(isIdentifierServerError('feature и непустой task обязательны')).toBe(true);
    expect(isIdentifierServerError('auto mode не поддерживается этим режимом запуска')).toBe(false);
  });
});
