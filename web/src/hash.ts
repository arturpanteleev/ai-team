export function shortenHash(value: string, visibleCharacters = 8): string {
  if (value.length <= visibleCharacters * 2 + 3) return value;
  return `${value.slice(0, visibleCharacters)}…${value.slice(-visibleCharacters)}`;
}
