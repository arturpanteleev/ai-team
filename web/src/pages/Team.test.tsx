import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Team } from './Team';

const session = vi.hoisted(() => ({ principal: { actor_id: 'owner@example.com', roles: ['product_owner'] } as { actor_id: string; roles: string[] } }));

vi.mock('../api', () => ({
  getActivePrincipal: () => session.principal,
  isAuthenticationRequired: () => true,
  getTeamAudit: vi.fn().mockResolvedValue({ events: [] }),
  getTeamMembers: vi.fn(),
  inviteTeamMember: vi.fn(),
  revokeTeamMember: vi.fn(),
  setTeamMemberRoles: vi.fn(),
}));

const members = [
  { actor_id: 'qa@example.com', email: 'qa@example.com', roles: ['qa'], status: 'active', updated_at: '' },
  { actor_id: 'root@example.com', email: 'root@example.com', roles: ['product_owner', 'admin'], status: 'active', updated_at: '' },
];

beforeEach(async () => {
  session.principal = { actor_id: 'owner@example.com', roles: ['product_owner'] };
  const api = await import('../api');
  vi.mocked(api.getTeamMembers).mockReset().mockResolvedValue({ members } as never);
  vi.mocked(api.setTeamMemberRoles).mockReset().mockResolvedValue({} as never);
  vi.mocked(api.revokeTeamMember).mockReset().mockResolvedValue({} as never);
  vi.mocked(api.inviteTeamMember).mockReset();
});

afterEach(cleanup);

describe('Team role editing', () => {
  it('edits roles with checkboxes, not a free-text prompt, and saves exactly the chosen roles', async () => {
    const api = await import('../api');
    const prompt = vi.spyOn(window, 'prompt');
    render(<Team />);

    fireEvent.click(await screen.findByRole('button', { name: 'Изменить роли: qa@example.com' }));
    const editor = screen.getByRole('group', { name: 'Роли: qa@example.com' });
    expect(within(editor).getByRole('checkbox', { name: /Тестировщик/ })).toBeChecked();
    fireEvent.click(within(editor).getByRole('checkbox', { name: /Ревьюер/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Сохранить роли' }));

    await waitFor(() => expect(api.setTeamMemberRoles).toHaveBeenCalledWith('qa@example.com', ['qa', 'reviewer']));
    expect(prompt).not.toHaveBeenCalled();
  });

  it('does not save an empty set of roles', async () => {
    const api = await import('../api');
    render(<Team />);

    fireEvent.click(await screen.findByRole('button', { name: 'Изменить роли: qa@example.com' }));
    const editor = screen.getByRole('group', { name: 'Роли: qa@example.com' });
    fireEvent.click(within(editor).getByRole('checkbox', { name: /Тестировщик/ }));
    expect(screen.getByRole('button', { name: 'Сохранить роли' })).toBeDisabled();
    expect(api.setTeamMemberRoles).not.toHaveBeenCalled();
  });

  it('lets only an administrator grant the admin role and says why to everybody else', async () => {
    render(<Team />);
    fireEvent.click(await screen.findByRole('button', { name: 'Изменить роли: qa@example.com' }));
    const editor = screen.getByRole('group', { name: 'Роли: qa@example.com' });
    expect(within(editor).getByRole('checkbox', { name: /Администратор/ })).toBeDisabled();
    expect(within(editor).getByText(/Право выдаёт и снимает только администратор/)).toBeInTheDocument();
    // The member who holds the admin right cannot be edited or revoked by a plain product owner.
    expect(screen.getByRole('button', { name: 'Изменить роли: root@example.com' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Отозвать доступ: root@example.com' })).toBeDisabled();
  });

  it('gives an administrator the admin checkbox', async () => {
    session.principal = { actor_id: 'root@example.com', roles: ['product_owner', 'admin'] };
    const api = await import('../api');
    render(<Team />);
    fireEvent.click(await screen.findByRole('button', { name: 'Изменить роли: qa@example.com' }));
    const editor = screen.getByRole('group', { name: 'Роли: qa@example.com' });
    const admin = within(editor).getByRole('checkbox', { name: /Администратор/ });
    expect(admin).toBeEnabled();
    fireEvent.click(admin);
    fireEvent.click(screen.getByRole('button', { name: 'Сохранить роли' }));
    await waitFor(() => expect(api.setTeamMemberRoles).toHaveBeenCalledWith('qa@example.com', ['qa', 'admin']));
  });

  it('shows the server refusal (last administrator) and keeps the editor open', async () => {
    session.principal = { actor_id: 'root@example.com', roles: ['product_owner', 'admin'] };
    const api = await import('../api');
    vi.mocked(api.setTeamMemberRoles).mockRejectedValueOnce(new Error('нельзя снять право администратора у последнего администратора'));
    render(<Team />);
    fireEvent.click(await screen.findByRole('button', { name: 'Изменить роли: root@example.com' }));
    const editor = screen.getByRole('group', { name: 'Роли: root@example.com' });
    fireEvent.click(within(editor).getByRole('checkbox', { name: /Администратор/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Сохранить роли' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('последнего администратора');
    expect(screen.getByRole('group', { name: 'Роли: root@example.com' })).toBeInTheDocument();
  });

  it('asks for confirmation inline before revoking access and never revokes oneself', async () => {
    const api = await import('../api');
    render(<Team />);
    fireEvent.click(await screen.findByRole('button', { name: 'Отозвать доступ: qa@example.com' }));
    const confirmation = screen.getByRole('group', { name: 'Отзыв доступа: qa@example.com' });
    fireEvent.click(within(confirmation).getByRole('button', { name: 'Отмена' }));
    expect(api.revokeTeamMember).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole('button', { name: 'Отозвать доступ: qa@example.com' }));
    fireEvent.click(within(screen.getByRole('group', { name: 'Отзыв доступа: qa@example.com' })).getByRole('button', { name: 'Да, отозвать доступ' }));
    await waitFor(() => expect(api.revokeTeamMember).toHaveBeenCalledWith('qa@example.com'));
  });
});
