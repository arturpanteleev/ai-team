import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Login } from './Login';

afterEach(cleanup);

describe('Login invitation activation', () => {
  const handlers = {
    onLogin: vi.fn().mockResolvedValue(undefined),
    onActivate: vi.fn().mockResolvedValue(undefined),
  };

  it('hides invitation activation in local mode', () => {
    render(<Login allowActivation={false} {...handlers} />);

    expect(screen.queryByText('Есть код приглашения?')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Активировать приглашение' })).not.toBeInTheDocument();
  });

  it('keeps invitation activation available for cloud auth', () => {
    render(<Login allowActivation {...handlers} />);

    expect(screen.getByText('Есть код приглашения?')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Активировать приглашение' })).toBeInTheDocument();
  });
});
