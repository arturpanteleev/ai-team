/// <reference types="node" />
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const css = readFileSync(`${process.cwd()}/src/index.css`, 'utf8');

const motionTokens = [
  '--glow-accent',
  '--glow-coral',
  '--glow-teal',
  '--duration-fast',
  '--duration-normal',
  '--duration-slow',
  '--duration-theme',
  '--ease-standard',
  '--ease-in',
  '--ease-out',
];

function declaredValue(block: string, token: string): string {
  return block.match(new RegExp(`${token}:\\s*([^;]+);`))?.[1]?.trim() ?? '';
}

describe('index.css theme tokens', () => {
  const darkIndex = css.indexOf(":root[data-theme='dark']");
  const lightBlock = css.slice(0, darkIndex);
  const darkBlock = css.slice(darkIndex);

  it('declares glow and motion tokens in both themes', () => {
    expect(darkIndex).toBeGreaterThan(0);
    for (const token of motionTokens) {
      expect(lightBlock).toContain(`${token}:`);
      expect(darkBlock).toContain(`${token}:`);
    }
  });

  it('makes the glow stronger in the dark theme', () => {
    for (const token of ['--glow-accent', '--glow-coral', '--glow-teal']) {
      const light = declaredValue(lightBlock, token);
      const dark = declaredValue(darkBlock, token);
      // Dark theme stacks two shadow layers; light theme has one.
      expect(dark.split(',').length).toBeGreaterThan(light.split(',').length);
      expect(light).not.toBe('');
    }
  });

  it('scopes the smooth theme transition to a temporary html class', () => {
    expect(css).toContain('html.theme-transition');
    expect(css).toContain('var(--duration-theme)');
  });

  it('disables animations and transitions under prefers-reduced-motion', () => {
    const reduced = css.slice(css.indexOf('@media (prefers-reduced-motion: reduce)'));
    expect(reduced).toContain('animation-duration: 0.01ms !important');
    expect(reduced).toContain('animation-iteration-count: 1 !important');
    expect(reduced).toContain('transition-duration: 0.01ms !important');
    expect(reduced).toContain('scroll-behavior: auto !important');
  });
});
