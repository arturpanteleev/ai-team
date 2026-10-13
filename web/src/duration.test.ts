import { describe, expect, it } from 'vitest';
import { formatStageDuration } from './duration';

const minute = 60_000;
const hour = 60 * minute;
const day = 24 * hour;

describe('formatStageDuration', () => {
  it.each([
    [Number.NaN, '—'],
    [Number.POSITIVE_INFINITY, '—'],
    [-1, '—'],
    [0, 'меньше минуты'],
    [59_999, 'меньше минуты'],
    [minute, '1 мин'],
    [59 * minute + 59_999, '59 мин'],
    [hour, '1 ч'],
    [hour + 5 * minute, '1 ч 5 мин'],
    [23 * hour + 59 * minute, '23 ч 59 мин'],
    [day, '1 д'],
    [day + 3 * hour + 59 * minute, '1 д 3 ч'],
    [10 * day, '10 д'],
  ])('%s мс → %s', (ms, expected) => {
    expect(formatStageDuration(ms)).toBe(expected);
  });
});
