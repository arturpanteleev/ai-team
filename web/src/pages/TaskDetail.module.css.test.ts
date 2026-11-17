/// <reference types="node" />
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const css = readFileSync(`${process.cwd()}/src/pages/TaskDetail.module.css`, 'utf8');

describe('TaskDetail neon stage strip (DES-03)', () => {
  it('uses only DES-02 glow and motion tokens for the neon effects', () => {
    for (const token of ['--glow-accent', '--duration-normal', '--duration-slow', '--ease-standard', '--ease-out']) {
      expect(css, `missing var(${token})`).toContain(`var(${token})`);
    }
  });

  it('lights the current stage, the conveyor rail and the pulsing dot', () => {
    expect(css).toContain(".routeItem[data-state='current'] .routeButton");
    expect(css).toMatch(/@keyframes\s+routeFlow/);
    expect(css).toMatch(/@keyframes\s+routePulse/);
    expect(css).toContain('animation: routeFlow var(--duration-slow)');
    expect(css).toContain('animation: routePulse var(--duration-slow)');
    expect((css.match(/box-shadow: var\(--glow-accent\)/g) ?? []).length).toBeGreaterThanOrEqual(3);
  });

  it('gives the single primary action a glow and a hover lift', () => {
    expect(css).toContain('.primary:not(:disabled)');
    expect(css).toContain('transform: translateY(-2px)');
    // CSS only decorates the existing primary button; it never creates a second one.
    expect(css).not.toMatch(/\.primary[^{}]*::(before|after)/);
  });

  it('hardcodes no colors: S16 color semantics stay in the theme tokens', () => {
    expect(css).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
    expect(css).not.toMatch(/\brgba?\(/);
  });

  it('disables the stage animations under prefers-reduced-motion', () => {
    const reduced = css.slice(css.indexOf('@media (prefers-reduced-motion: reduce)'));
    expect(reduced).toContain('.routeItem::before');
    expect(reduced).toContain(".routeItem[data-state='current'] .routeDot");
    expect(reduced).toContain('animation: none');
  });
});
