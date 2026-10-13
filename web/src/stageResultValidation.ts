// Client-side checks for human stage results. The server (pkg/humanartifact)
// stays authoritative; these give early feedback in the browser.

// Limits mirror pkg/humanartifact: MaxContentBytes, MaxCommentBytes and the
// link URL limit.
export const MAX_MARKDOWN_BYTES = 10 << 20;
export const MAX_TEXT_BYTES = 16 << 10;
export const MAX_URL_BYTES = 16 << 10;

export type LinkKind = 'pr' | 'build' | 'other';

export interface StageResultSubmission {
  result: 'md' | 'link' | 'approve';
  content?: string;
  url?: string;
  link_kind?: LinkKind;
  note?: string;
  description?: string;
}

export const linkKindTitles: Record<LinkKind, string> = { pr: 'Pull request', build: 'Сборка', other: 'Другое' };

export function utf8Length(value: string): number {
  let bytes = 0;
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    if (code < 0x80) bytes += 1;
    else if (code < 0x800) bytes += 2;
    else if (code >= 0xd800 && code <= 0xdbff && index + 1 < value.length &&
      value.charCodeAt(index + 1) >= 0xdc00 && value.charCodeAt(index + 1) <= 0xdfff) {
      bytes += 4;
      index += 1;
    } else bytes += 3;
  }
  return bytes;
}

export function formatBytes(bytes: number): string {
  if (bytes >= 1 << 20) return `${(bytes / (1 << 20)).toFixed(1)} МиБ`;
  if (bytes >= 1 << 10) return `${(bytes / (1 << 10)).toFixed(1)} КиБ`;
  return `${bytes} Б`;
}

const HTTP_PREFIX = /^https?:\/\/[^\s/?#]/i;
const OTHER_SCHEME = /^[a-z][a-z0-9+.-]*:\/\//i;

// hasHttpPrefix reports whether value literally starts with http:// or https://
// followed by a host.
export function hasHttpPrefix(value: string): boolean {
  return HTTP_PREFIX.test(value);
}

// linkError returns why a URL cannot be submitted, matching the server's
// HTTP(S)-only rule; credentials in the URL are rejected so they never land in
// task history.
export function linkError(value: string): string {
  const trimmed = value.trim();
  if (!trimmed) return '';
  if (utf8Length(trimmed) > MAX_URL_BYTES) return `Ссылка длиннее ${MAX_URL_BYTES} байт.`;
  // new URL() alone accepts forms like "https:host" or "http:/host"; the
  // literal prefix keeps the stored link exactly as people will click it.
  if (!hasHttpPrefix(trimmed)) {
    return OTHER_SCHEME.test(trimmed) && !/^https?:/i.test(trimmed)
      ? 'Ссылка должна начинаться с http:// или https://.'
      : 'Введите полный адрес, начиная с https://.';
  }
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return 'Введите полный адрес, начиная с https://.';
  }
  if (parsed.protocol !== 'https:' && parsed.protocol !== 'http:') return 'Ссылка должна начинаться с http:// или https://.';
  if (!parsed.hostname) return 'В ссылке нет адреса сервера.';
  if (parsed.username || parsed.password) return 'Уберите логин и пароль из ссылки.';
  return '';
}

export function linkHint(value: string, kind: LinkKind | undefined): string {
  if (kind !== 'pr' || !value.trim() || linkError(value)) return '';
  try {
    if (/\/(pull|pulls|merge_requests)\/\d+/.test(new URL(value.trim()).pathname)) return '';
  } catch {
    return '';
  }
  return 'Ссылка не похожа на pull request; проверка этапа может её отклонить.';
}
