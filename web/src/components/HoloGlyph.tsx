import type { CSSProperties } from 'react';
import styles from './HoloGlyph.module.css';

export type HoloState = 'empty' | 'complete' | 'decision';

// DES-06: характер микродвижения из референса (вещество «собирается» из линий,
// вокруг светятся траектории), но не его пастельная палитра. Все цвета — токены
// темы/DES-02, заданные в HoloGlyph.module.css; S16-семантика не меняется.
// Глиф декоративен: aria-hidden, без tabindex и без интерактивных потомков —
// DOM-порядок и порядок фокуса страниц остаются прежними.
export function HoloGlyph({ state, size = 'sm', block = false }: {
  state: HoloState;
  size?: 'sm' | 'md';
  block?: boolean;
}) {
  const className = [styles.glyph, styles[state], size === 'md' ? styles.md : '', block ? styles.block : '']
    .filter(Boolean).join(' ');

  // --dash — длина конкретного path (с запасом не более 3 ед.): она же становится
  // dash pattern и стартовым dashoffset, пока линия «собирается» по holo-draw.
  return (
    <span className={className} aria-hidden="true" data-holo={state} data-testid="holo-glyph">
      <svg viewBox="0 0 48 48" fill="none" stroke="currentColor" strokeWidth="2"
        strokeLinecap="round" strokeLinejoin="round" focusable="false">
        {state === 'empty' && <>
          <circle className={styles.dots} cx="24" cy="24" r="18" />
          <path className={styles.arc} style={{ '--dash': 30 } as CSSProperties} d="M24 6a18 18 0 0 1 18 18" />
          <path className={styles.brackets} style={{ '--dash': 42 } as CSSProperties} d="M9 14V9h5M39 14V9h-5M9 34v5h5M39 34v5h-5" />
          <line className={styles.scan} x1="24" y1="24" x2="24" y2="9" />
          <circle cx="24" cy="24" r="2" fill="currentColor" stroke="none" />
        </>}
        {state === 'complete' && <>
          <circle className={styles.ring} style={{ '--dash': 114 } as CSSProperties} cx="24" cy="24" r="18" />
          <path className={styles.check} style={{ '--dash': 30 } as CSSProperties} d="M14 24.5l6.5 6.5L34 16.5" />
          <circle className={styles.spark} cx="24" cy="24" r="21" />
        </>}
        {state === 'decision' && <>
          <path className={styles.orbit} d="M24 6a18 18 0 0 1 18 18" />
          <path className={styles.orbitAlt} d="M24 42a18 18 0 0 1-18-18" />
          <path className={styles.brackets} style={{ '--dash': 24 } as CSSProperties} d="M24 14l4 4-4 4-4-4z" />
          <circle className={styles.core} cx="24" cy="24" r="2" fill="currentColor" stroke="none" />
        </>}
      </svg>
    </span>
  );
}
