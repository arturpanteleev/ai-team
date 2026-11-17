import { cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { HoloGlyph, type HoloState } from './HoloGlyph';

afterEach(cleanup);

const states: HoloState[] = ['empty', 'complete', 'decision'];

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
