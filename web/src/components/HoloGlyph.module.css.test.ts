/// <reference types="node" />
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const css = readFileSync(`${process.cwd()}/src/components/HoloGlyph.module.css`, 'utf8');

describe('HoloGlyph holographic materialization (DES-06)', () => {
  it('takes every colour and duration from theme/DES-02 tokens', () => {
    for (const token of [
      '--text-secondary', '--success-fg', '--ink-teal',
      '--glow-accent', '--glow-teal',
      '--duration-fast', '--duration-slow', '--ease-standard', '--ease-out',
    ]) {
      expect(css, `missing var(${token})`).toContain(`var(${token})`);
    }
  });

  it('keeps S16 meanings: empty neutral, completed green, awaiting teal', () => {
    expect(css).toContain('.empty');
    expect(css).toContain('color: var(--text-secondary)');
    expect(css).toContain('.complete');
    expect(css).toContain('color: var(--success-fg)');
    expect(css).toContain('.decision');
    expect(css).toContain('color: var(--ink-teal)');
  });

  it('materializes lines and sweeps a faint trajectory, not a pastel rocket', () => {
    for (const name of ['holo-draw', 'holo-sweep', 'holo-pulse', 'holo-bloom', 'holo-orbit']) {
      expect(css, `missing @keyframes ${name}`).toMatch(new RegExp(`@keyframes\\s+${name}`));
    }
    expect(css).toContain('animation: holo-draw var(--duration-slow)');
    expect(css).toContain('animation: holo-sweep calc(var(--duration-slow) * 4)');
    // One quiet continuous motion at a time; the rest happens once on mount.
    expect(css).toContain('animation: holo-bloom var(--duration-slow)');
  });

  it('hardcodes no colours: S16 colour semantics stay in the theme tokens', () => {
    expect(css).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
    expect(css).not.toMatch(/\brgba?\(/);
  });

  it('stops every animation and glow under prefers-reduced-motion', () => {
    const reduced = css.slice(css.indexOf('@media (prefers-reduced-motion: reduce)'));
    expect(reduced).toContain('animation: none');
    expect(reduced).toContain('.ring, .arc, .scan, .core, .check, .spark, .orbit, .orbitAlt, .brackets');
    expect(reduced).toContain('filter: none');
    // The resting styles are the visible end state, so no motion means no missing glyph.
    expect(css.slice(0, css.indexOf('@media (prefers-reduced-motion: reduce)'))).toContain('stroke-dashoffset: 0');
  });
});
