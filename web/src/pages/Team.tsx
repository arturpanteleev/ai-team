import { useEffect, useState } from 'react';
import { getTeamAudit, getTeamMembers, inviteTeamMember, revokeTeamMember, setTeamMemberRoles } from '../api';
import type { TeamAuditEvent, TeamMember } from '../api';
import { usePageTitle } from '../hooks/usePageTitle';
import styles from './Team.module.css';

const roleLabels: Record<string, string> = {
  product_owner: 'Владелец продукта',
  architect: 'Архитектор',
  developer: 'Разработчик',
  reviewer: 'Ревьюер',
  qa: 'Тестировщик',
  release_manager: 'Менеджер выпуска',
};
const roles = Object.keys(roleLabels);
const memberStatusLabels: Record<string, string> = {
  invited: 'приглашён',
  active: 'активен',
  revoked: 'доступ отозван',
};

export function Team() {
  usePageTitle('Команда');
  const [members, setMembers] = useState<TeamMember[]>([]);
  const [audit, setAudit] = useState<TeamAuditEvent[]>([]);
  const [email, setEmail] = useState('');
  const [selected, setSelected] = useState<string[]>(['product_owner']);
  const [invite, setInvite] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const refresh = async () => {
    const [memberData, auditData] = await Promise.all([getTeamMembers(), getTeamAudit()]);
    setMembers(memberData.members);
    setAudit(auditData.events);
  };

  useEffect(() => {
    void refresh().catch((value: unknown) => setError(value instanceof Error ? value.message : 'Не удалось загрузить команду.'));
  }, []);

  const toggleRole = (role: string) => setSelected((current) =>
    current.includes(role) ? current.filter((item) => item !== role) : [...current, role]);

  const createInvite = async (event: React.FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError('');
    setInvite('');
    try {
      const value = await inviteTeamMember(email, selected);
      setInvite(value.activation_token);
      setEmail('');
      await refresh();
    } catch (value) {
      setError(value instanceof Error ? value.message : 'Не удалось создать приглашение.');
    } finally {
      setBusy(false);
    }
  };

  const editRoles = async (member: TeamMember) => {
    const input = window.prompt(`Роли для ${member.email}, через запятую`, member.roles.join(', '));
    if (input === null) return;
    const nextRoles = input.split(',').map((value) => value.trim()).filter(Boolean);
    if (nextRoles.length === 0) {
      setError('Укажите хотя бы одну роль.');
      return;
    }
    setBusy(true);
    setError('');
    try {
      await setTeamMemberRoles(member.actor_id, nextRoles);
      await refresh();
    } catch (value) {
      setError(value instanceof Error ? value.message : 'Не удалось изменить роли.');
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (member: TeamMember) => {
    if (!window.confirm(`Отозвать доступ у ${member.email}?`)) return;
    setBusy(true);
    setError('');
    try {
      await revokeTeamMember(member.actor_id);
      await refresh();
    } catch (value) {
      setError(value instanceof Error ? value.message : 'Не удалось отозвать доступ.');
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className={styles.page}>
      <h1>Команда</h1>
      <p>Участники одного рабочего пространства. Изменение ролей и отзыв доступа завершают действующие веб-сессии.</p>
      {error && <p role="alert" className={styles.error}>{error}</p>}
      <form onSubmit={createInvite} className={styles.invite}>
        <h2>Пригласить участника</h2>
        <label htmlFor="invite-email">Электронная почта
          <input id="invite-email" type="email" required value={email} onChange={(event) => setEmail(event.target.value)} />
        </label>
        <fieldset>
          <legend>Роли</legend>
          {roles.map((role) => (
            <label key={role}>
              <input type="checkbox" checked={selected.includes(role)} onChange={() => toggleRole(role)} />
              {roleLabels[role]}
            </label>
          ))}
        </fieldset>
        <button disabled={busy || selected.length === 0}>{busy ? 'Сохраняем…' : 'Создать приглашение'}</button>
      </form>
      {invite && (
        <div className={styles.invitation}>
          <strong>Передайте этот одноразовый код участнику:</strong>
          <code>{invite}</code>
          <small>Срок действия — 48 часов. Код показывается только сейчас.</small>
        </div>
      )}
      <h2>Участники</h2>
      <div className={styles.members}>
        {members.length === 0 && <p>Пока нет участников.</p>}
        {members.map((member) => (
          <article key={member.actor_id}>
            <div>
              <strong>{member.email}</strong>
              <small>{memberStatusLabels[member.status] ?? member.status} · {member.roles.map((role) => roleLabels[role] ?? role).join(', ') || 'без ролей'}</small>
            </div>
            <div>
              <button disabled={busy || member.actor_id === ''} onClick={() => void editRoles(member)}>Изменить роли</button>
              <button disabled={busy || member.status === 'revoked'} onClick={() => void revoke(member)}>Отозвать доступ</button>
            </div>
          </article>
        ))}
      </div>
      <h2>Журнал действий</h2>
      {audit.length === 0 ? <p>Записей пока нет.</p> : (
        <ol className={styles.audit}>
          {audit.map((item) => (
            <li key={item.id}>
              <time>{new Date(item.created_at).toLocaleString('ru-RU')}</time>
              <span>{item.actor_id}: {item.action} → {item.target_actor_id}</span>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
