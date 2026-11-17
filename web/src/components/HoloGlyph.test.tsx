import { cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { HoloGlyph, type HoloState } from './HoloGlyph';

afterEach(cleanup);

const states: HoloState[] = ['empty', 'complete', 'decision'];

// Длины path в viewBox 0 0 48 48 (с округлением вверх): дуга 28.3, скобки 40,
// кольцо 113.1, галочка 29, ромб 22.6. --dash короче path оставляет линию
// видимой в первом кадре, а намного длиннее — рисует её лишь часть анимации.
const drawnLengths: Record<HoloState, Record<string, [number, number]>> = {
  empty: { arc: [28.3, 31.3], brackets: [40, 43] },
  complete: { ring: [113.1, 116.1], check: [29, 32] },
  decision: { brackets: [22.6, 25.6] },
};

describe('HoloGlyph (DES-06)', () => {
  it('renders one distinct decorative mark for each of the three states', () => {
    for (const state of states) {
      const { container, unmount } = render(<HoloGlyph state={state} />);
      const glyph = container.querySelector('[data-testid="holo-glyph"]')!;
      expect(glyph).toHaveAttribute('data-holo', state);
      expect(glyph).toHaveAttribute('aria-hidden', 'true');
      expect(glyph.querySelectorAll('svg')).toHaveLength(1);
      expect(glyph.textContent).toBe('');
      unmount();
    }
  });

  it('gives every drawn path a dash as long as the path itself', () => {
    for (const state of states) {
      const { container, unmount } = render(<HoloGlyph state={state} />);
      for (const [name, [min, max]] of Object.entries(drawnLengths[state])) {
        const target = container.querySelector(`[class*="${name}"]`)!;
        const dash = Number(/--dash:\s*([\d.]+)/.exec(target.getAttribute('style') ?? '')?.[1]);
        expect(Number.isFinite(dash), `${state}/${name} has no --dash`).toBe(true);
        expect(dash, `${state}/${name} hides nothing at the first frame`).toBeGreaterThanOrEqual(min);
        expect(dash, `${state}/${name} draws only part of the path`).toBeLessThanOrEqual(max);
      }
      unmount();
    }
  });

  it('never adds a focusable element or a button: focus and DOM order stay intact', () => {
    for (const state of states) {
      const { container, unmount } = render(<HoloGlyph state={state} />);
      const glyph = container.querySelector('[data-testid="holo-glyph"]')!;
      expect(glyph.querySelector('button, a, input, select, textarea, [tabindex], [contenteditable]')).toBeNull();
      expect(glyph.querySelector('svg')).toHaveAttribute('focusable', 'false');
      unmount();
    }
  });

  it('keeps the same namespace for the small inline and the block variants', () => {
    const { container } = render(<>
      <HoloGlyph state="decision" />
      <HoloGlyph state="empty" size="md" block />
    </>);
    const glyphs = container.querySelectorAll('[data-testid="holo-glyph"]');
    expect(glyphs).toHaveLength(2);
    expect(glyphs[0].getAttribute('class')).not.toBe(glyphs[1].getAttribute('class'));
  });
});
