/// <reference types="node" />
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const css = readFileSync(`${process.cwd()}/src/components/PipelineCard.module.css`, 'utf8');

describe('PipelineCard volumetric card (DES-05)', () => {
  it('uses only DES-02 glow and motion tokens', () => {
    for (const token of ['--glow-accent', '--glow-coral', '--glow-teal', '--duration-normal', '--ease-standard', '--ease-out']) {
      expect(css, `missing var(${token})`).toContain(`var(${token})`);
    }
  });

  it('draws a gradient status strip from S16 tint/ink pairs', () => {
    expect(css).toMatch(/\.card::before/);
    expect(css).toContain('linear-gradient(180deg, var(--tint-teal), var(--ink-teal))');
    expect(css).toContain('linear-gradient(180deg, var(--tint-yellow), var(--ink-yellow))');
    expect(css).toContain('linear-gradient(180deg, var(--tint-coral), var(--ink-coral))');
    expect(css).toContain('linear-gradient(180deg, var(--tint-mustard), var(--ink-mustard))');
  });

  it('stacks layered shadows with a neon glow and lifts on hover', () => {
    expect(css).toContain('box-shadow: var(--shadow-soft)');
    expect(css).toContain('box-shadow: var(--shadow), var(--glow-accent)');
    expect(css).toContain('box-shadow: var(--shadow), var(--glow-coral)');
    expect(css).toContain('box-shadow: var(--shadow), var(--glow-teal)');
    expect(css).toContain('transform: translateY(-2px)');
  });

  it('hardcodes no colors: S16 color semantics stay in the theme tokens', () => {
    expect(css).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
    expect(css).not.toMatch(/\brgba?\(/);
  });

  it('keeps the focus ring and disables the lift under prefers-reduced-motion', () => {
    expect(css).toContain('.card:focus-visible');
    expect(css).toContain('outline: 3px solid var(--focus)');
    const reduced = css.slice(css.indexOf('@media (prefers-reduced-motion: reduce)'));
    expect(reduced).toContain('.card:hover');
    expect(reduced).toContain('transform: none');
  });
});
