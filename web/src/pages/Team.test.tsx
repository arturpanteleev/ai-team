import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Team } from './Team';

vi.mock('../api', () => ({
  getTeamAudit: vi.fn().mockResolvedValue({ events: [] }),
  getTeamMembers: vi.fn().mockResolvedValue({ members: [{ actor_id: 'qa@example.com', email: 'qa@example.com', roles: ['qa'], status: 'active' }] }),
  inviteTeamMember: vi.fn(),
  revokeTeamMember: vi.fn(),
  setTeamMemberRoles: vi.fn(),
}));

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('Team role editing', () => {
  it('rejects an empty role prompt without calling the API', async () => {
    const api = await import('../api');
    vi.mocked(api.setTeamMemberRoles).mockClear();
    vi.spyOn(window, 'prompt').mockReturnValue(' ,  ');
    render(<Team />);

    fireEvent.click(await screen.findByRole('button', { name: 'Изменить роли' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('Укажите хотя бы одну роль.');
    expect(api.setTeamMemberRoles).not.toHaveBeenCalled();
    await waitFor(() => expect(api.getTeamMembers).toHaveBeenCalledTimes(1));
  });
});
