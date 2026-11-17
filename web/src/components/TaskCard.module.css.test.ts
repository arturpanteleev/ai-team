/// <reference types="node" />
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const css = readFileSync(`${process.cwd()}/src/components/TaskCard.module.css`, 'utf8');

describe('TaskCard volumetric card (DES-05)', () => {
  it('uses only DES-02 glow and motion tokens', () => {
    for (const token of ['--glow-accent', '--glow-coral', '--glow-teal', '--duration-normal', '--ease-standard', '--ease-out']) {
      expect(css, `missing var(${token})`).toContain(`var(${token})`);
    }
  });

  it('derives the gradient status strip from the existing chip tone, not new semantics', () => {
    expect(css).toMatch(/\.card::before/);
    for (const tone of ['yellow', 'mustard', 'coral', 'green', 'blue', 'danger']) {
      expect(css).toContain(`.card:has(.chip[data-tone='${tone}'])::before`);
    }
    expect(css).toContain('linear-gradient(180deg, var(--tint-coral), var(--ink-coral))');
    expect(css).toContain('linear-gradient(180deg, var(--danger-bg), var(--danger-fg))');
  });

  it('stacks shadows with a neon glow and lifts on hover', () => {
    expect(css).toContain('box-shadow: var(--shadow-soft)');
    expect(css).toContain('box-shadow: var(--shadow), var(--glow-accent)');
    expect(css).toContain('box-shadow: var(--shadow), var(--glow-coral)');
    expect(css).toContain('transform: translateY(-2px)');
  });

  it('hardcodes no colors: S16 color semantics stay in the theme tokens', () => {
    expect(css).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
    expect(css).not.toMatch(/\brgba?\(/);
  });

  it('keeps the focus ring and disables the lift under prefers-reduced-motion', () => {
    expect(css).toContain('.card:focus-visible');
    expect(css).toContain('outline: 3px solid var(--accent)');
    const reduced = css.slice(css.indexOf('@media (prefers-reduced-motion: reduce)'));
    expect(reduced).toContain('.card:hover');
    expect(reduced).toContain('transform: none');
  });
});
