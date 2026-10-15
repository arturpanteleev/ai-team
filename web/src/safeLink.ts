import { hasHttpPrefix } from './stageResultValidation';

// A link taken from run data is rendered as an anchor only when it is a plain
// http(s) URL without credentials; anything else (javascript:, data:, user:pass@)
// stays text.
export function httpLink(value: string): boolean {
  if (!hasHttpPrefix(value)) return false;
  try {
    const url = new URL(value);
    return (url.protocol === 'https:' || url.protocol === 'http:') && !url.username && !url.password;
  } catch {
    return false;
  }
}
