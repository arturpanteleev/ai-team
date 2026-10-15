import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { McpAccess } from './McpAccess';
import * as api from '../api';

vi.mock('../api', () => ({
  listMcpTokens: vi.fn(),
  issueMcpToken: vi.fn(),
  revokeMcpToken: vi.fn(),
}));

const live = { id: 't1', label: 'laptop', hint: 'wxyz', created_at: '2026-10-15T00:00:00Z', expires_at: '2099-01-01T00:00:00Z' };

beforeEach(() => {
  vi.mocked(api.listMcpTokens).mockResolvedValue({ tokens: [], max_active: 20 });
  vi.mocked(api.issueMcpToken).mockResolvedValue({ ...live, token: 'aitm_secret' });
  vi.mocked(api.revokeMcpToken).mockResolvedValue(undefined);
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.clearAllMocks();
});

describe('McpAccess', () => {
  it('shows a new token once, in a live region, and never after a reload', async () => {
    const { unmount } = render(<McpAccess />);
    fireEvent.change(await screen.findByLabelText(/Название/), { target: { value: 'laptop' } });
    fireEvent.click(screen.getByRole('button', { name: 'Выпустить токен' }));
    expect(await screen.findByText('aitm_secret')).toBeInTheDocument();
    await waitFor(() => expect(api.issueMcpToken).toHaveBeenCalledWith('laptop', 90));
    const region = screen.getByRole('status');
    expect(region).toHaveAttribute('aria-live', 'polite');
    expect(region).toHaveTextContent('aitm_secret');

    unmount();
    vi.mocked(api.listMcpTokens).mockResolvedValue({ tokens: [live], max_active: 20 });
    render(<McpAccess />);
    expect(await screen.findByText('laptop')).toBeInTheDocument();
    expect(screen.queryByText('aitm_secret')).not.toBeInTheDocument();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
  });

  it('revokes a token after confirmation using a button named after the token', async () => {
    vi.mocked(api.listMcpTokens).mockResolvedValueOnce({ tokens: [live], max_active: 20 })
      .mockResolvedValue({ tokens: [{ ...live, revoked_at: '2026-10-15T01:00:00Z' }], max_active: 20 });
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true);
    render(<McpAccess />);
    fireEvent.click(await screen.findByRole('button', { name: 'Отозвать токен «laptop»' }));
    await waitFor(() => expect(api.revokeMcpToken).toHaveBeenCalledWith('t1'));
    expect(confirm).toHaveBeenCalled();
    await waitFor(() => expect(screen.getByRole('button', { name: 'Отозвать токен «laptop»' })).toBeDisabled());
    expect(screen.getByText(/отозван/)).toBeInTheDocument();
  });

  it('does not revoke when the confirmation is declined', async () => {
    vi.mocked(api.listMcpTokens).mockResolvedValue({ tokens: [live], max_active: 20 });
    vi.spyOn(window, 'confirm').mockReturnValue(false);
    render(<McpAccess />);
    fireEvent.click(await screen.findByRole('button', { name: 'Отозвать токен «laptop»' }));
    expect(api.revokeMcpToken).not.toHaveBeenCalled();
  });
});
