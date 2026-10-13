import type { ReactNode } from 'react';
import styles from './Bubble.module.css';

export type BubbleTone = 'yellow' | 'mint' | 'coral' | 'mustard' | 'blue';

// Пастельная плашка-«пузырь» с градиентом и бликом под иконкой. Декоративная: подпись рядом с ней.
export function Bubble({ tone, size = 'md', className, children }: {
  tone: BubbleTone;
  size?: 'sm' | 'md';
  className?: string;
  children: ReactNode;
}) {
  return (
    <span className={[styles.bubble, styles[tone], size === 'sm' ? styles.sm : '', className ?? ''].join(' ').trim()}
      aria-hidden="true">
      {children}
    </span>
  );
}
