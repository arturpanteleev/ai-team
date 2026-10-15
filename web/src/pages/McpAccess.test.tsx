import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { McpAccess } from './McpAccess';

vi.mock('../api', () => ({
  listMcpTokens: vi.fn().mockResolvedValue({ tokens: [], max_active: 20 }),
  issueMcpToken: vi.fn().mockResolvedValue({ id: 'a', label: 'laptop', hint: 'wxyz', token: 'aitm_secret', created_at: '2026-10-15T00:00:00Z', expires_at: '2027-01-01T00:00:00Z' }),
  revokeMcpToken: vi.fn(),
}));

afterEach(cleanup);

describe('McpAccess', () => {
  it('shows a new token once after issuing it', async () => {
    const api = await import('../api');
    render(<McpAccess />);
    fireEvent.change(await screen.findByLabelText(/Название/), { target: { value: 'laptop' } });
    fireEvent.click(screen.getByRole('button', { name: 'Выпустить токен' }));
    expect(await screen.findByText('aitm_secret')).toBeInTheDocument();
    await waitFor(() => expect(api.issueMcpToken).toHaveBeenCalledWith('laptop', 90));
  });
});
