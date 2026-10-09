import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { shortenHash } from '../hash';
import { CopyableHash } from './CopyableHash';

describe('CopyableHash', () => {
  it('shortens long values but copies the complete hash', async () => {
    const value = '12345678abcdef0090abcdef';
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText },
    });

    render(<CopyableHash value={value} label="хеш решения" />);
    expect(screen.getByText(shortenHash(value))).toHaveTextContent('12345678…90abcdef');
    fireEvent.click(screen.getByRole('button', { name: 'Скопировать хеш решения' }));

    expect(writeText).toHaveBeenCalledWith(value);
    expect(await screen.findByRole('status')).toHaveTextContent('Скопировано');
  });
});
