import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { StatusBadge } from './StatusBadge';
import styles from './StatusBadge.module.css';

describe('StatusBadge', () => {
  it.each(['skipped', 'interrupted'] as const)('renders the %s state explicitly', (status) => {
    render(<StatusBadge status={status} />);
    expect(screen.getByText(status)).toHaveClass(styles[status]);
  });
});
