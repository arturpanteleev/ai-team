/// <reference types="node" />
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const css = readFileSync(`${process.cwd()}/src/components/Bubble.module.css`, 'utf8');

describe('Bubble pastel shimmer (DES-05)', () => {
  it('uses only DES-02 glow and motion tokens for the effects', () => {
    for (const token of ['--glow-accent', '--glow-coral', '--glow-teal', '--duration-slow', '--ease-standard']) {
      expect(css, `missing var(${token})`).toContain(`var(${token})`);
    }
  });

  it('shimmers the pastel gradient token without hardcoding a color', () => {
    expect(css).toMatch(/@keyframes\s+bubble-sheen/);
    expect(css).toContain('animation: bubble-sheen var(--duration-slow)');
    expect(css).toContain('background-size: 180% 180%');
    for (const tone of ['yellow', 'mint', 'coral', 'mustard', 'blue']) {
      expect(css).toContain(`background-image: var(--bubble-${tone})`);
    }
  });

  it('uses layered shadows and a tone-matched neon glow', () => {
    expect(css).toContain('box-shadow: var(--shadow-soft), var(--glow-accent)');
    expect(css).toContain('box-shadow: var(--shadow-soft), var(--glow-teal)');
    expect(css).toContain('box-shadow: var(--shadow-soft), var(--glow-coral)');
  });

  it('hardcodes no colors: S16 color semantics stay in the theme tokens', () => {
    expect(css).not.toMatch(/#[0-9a-fA-F]{3,8}\b/);
    expect(css).not.toMatch(/\brgba?\(/);
  });

  it('stops the shimmer under prefers-reduced-motion', () => {
    const reduced = css.slice(css.indexOf('@media (prefers-reduced-motion: reduce)'));
    expect(reduced).toContain('.bubble');
    expect(reduced).toContain('animation: none');
  });
});
