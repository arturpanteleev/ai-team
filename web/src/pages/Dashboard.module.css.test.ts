/// <reference types="node" />
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const css = readFileSync(`${process.cwd()}/src/pages/Dashboard.module.css`, 'utf8');

describe('Dashboard volumetric surfaces (DES-05)', () => {
  it('uses only DES-02 glow and motion tokens', () => {
    for (const token of ['--glow-accent', '--glow-coral', '--glow-teal', '--active-gradient', '--active-shadow', '--duration-normal', '--ease-out']) {
      expect(css, `missing var(${token})`).toContain(`var(${token})`);
    }
  });

  it('softly highlights the active filter without adding a second primary button', () => {
    expect(css).toContain('.filterBtn.active');
    expect(css).toContain('background: var(--active-gradient)');
    expect(css).toContain('box-shadow: var(--active-shadow), var(--glow-accent)');
    // Only CSS decorates the existing aria-pressed control; no new button styles.
    expect(css).not.toMatch(/\.filterBtn\.active[^{}]*::(before|after)/);
  });

  it('lifts the single primary button with a token glow', () => {
    expect(css).toContain('box-shadow: var(--shadow), var(--glow-accent)');
    expect(css).toContain('transform: translateY(-2px)');
    expect(css).toContain('var(--create-yellow)');
  });

  it('keeps S16 readiness semantics while adding token glows', () => {
    expect(css).toContain(".readiness[data-state='ready'] .dot");
    expect(css).toContain('background: var(--success-fg)');
    expect(css).toContain('var(--glow-teal)');
    expect(css).toContain(".readiness[data-state='blocked'] .dot");
    expect(css).toContain('background: var(--danger-fg)');
    expect(css).toContain('var(--glow-coral)');
    expect(css).toContain(".readiness[data-state='unknown'] .dot");
    expect(css).toContain('background: var(--text-muted)');
  });

  it('hardcodes no colors: S16 color semantics stay in the theme tokens', () => {
    expect(css).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
    expect(css).not.toMatch(/\brgba?\(/);
  });

  it('disables hover lifts and transitions under prefers-reduced-motion', () => {
    const reduced = css.slice(css.indexOf('@media (prefers-reduced-motion: reduce)'));
    expect(reduced).toContain('.createBig:hover');
    expect(reduced).toContain('transform: none');
  });
});
