import { useEffect, useState } from 'react';
import { getActivePrincipal, getTeamAudit, getTeamMembers, inviteTeamMember, revokeTeamMember, setTeamMemberRoles } from '../api';
import type { TeamAuditEvent, TeamMember } from '../api';
import { mayPerform, roleLabels } from '../permissions';
import { usePageTitle } from '../hooks/usePageTitle';
import styles from './Team.module.css';

// Роли процесса и одна системная: «Администратор» не функция этапа, а право
// провести задачу после проваленного обязательного гейта (S06).
const roles = Object.keys(roleLabels);
const adminHelp = 'Администратор может провести задачу дальше после проваленного обязательного гейта; обход всегда остаётся красным в истории. Право выдаёт и снимает только администратор.';
const memberStatusLabels: Record<string, string> = {
  invited: 'приглашён',
  active: 'активен',
  revoked: 'доступ отозван',
};
const auditActionLabels: Record<string, string> = {
  bootstrap: 'создал команду',
  roles_changed: 'изменил роли',
  membership_revoked: 'отозвал доступ',
  invited: 'пригласил',
  member_invited: 'пригласил',
  member_activated: 'активировал приглашение',
};

function RoleChecklist({ legend, selected, onToggle, canGrantAdmin, name }: {
  legend: string;
  selected: string[];
  onToggle: (role: string) => void;
  canGrantAdmin: boolean;
  name: string;
}) {
  return (
    <fieldset>
      <legend>{legend}</legend>
      {roles.map((role) => (
        <label key={role} title={role === 'admin' ? adminHelp : undefined}>
          <input type="checkbox" name={name} checked={selected.includes(role)} onChange={() => onToggle(role)}
            disabled={role === 'admin' && !canGrantAdmin} />
          {roleLabels[role]}{role === 'admin' ? ' (обход проваленных гейтов)' : ''}
        </label>
      ))}
      {!canGrantAdmin && <small className={styles.adminNote}>{adminHelp}</small>}
    </fieldset>
  );
}

export function Team() {
  usePageTitle('Команда');
  const principal = getActivePrincipal();
  const canGrantAdmin = mayPerform(principal, 'gate:override');
  const [members, setMembers] = useState<TeamMember[]>([]);
  const [audit, setAudit] = useState<TeamAuditEvent[]>([]);
  const [email, setEmail] = useState('');
  const [selected, setSelected] = useState<string[]>(['product_owner']);
  const [invite, setInvite] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<{ actorId: string; roles: string[] } | null>(null);
  const [revoking, setRevoking] = useState('');

  const refresh = async () => {
    const [memberData, auditData] = await Promise.all([getTeamMembers(), getTeamAudit()]);
    setMembers(memberData.members);
    setAudit(auditData.events);
  };

  useEffect(() => {
    void refresh().catch((value: unknown) => setError(value instanceof Error ? value.message : 'Не удалось загрузить команду.'));
  }, []);

  const toggle = (current: string[], role: string) =>
    current.includes(role) ? current.filter((item) => item !== role) : [...current, role];

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

  const saveRoles = async () => {
    if (!editing) return;
    if (editing.roles.length === 0) {
      setError('Укажите хотя бы одну роль.');
      return;
    }
    setBusy(true);
    setError('');
    try {
      await setTeamMemberRoles(editing.actorId, editing.roles);
      setEditing(null);
      await refresh();
    } catch (value) {
      setError(value instanceof Error ? value.message : 'Не удалось изменить роли.');
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (member: TeamMember) => {
    setBusy(true);
    setError('');
    try {
      await revokeTeamMember(member.actor_id);
      setRevoking('');
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
        <RoleChecklist legend="Роли" name="invite-role" selected={selected} canGrantAdmin={canGrantAdmin}
          onToggle={(role) => setSelected((current) => toggle(current, role))} />
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
        {members.map((member) => {
          const isEditing = editing?.actorId === member.actor_id;
          const isSelf = principal?.actor_id === member.actor_id;
          const holdsAdmin = member.roles.includes('admin');
          return (
            <article key={member.actor_id} data-editing={isEditing ? 'true' : undefined}>
              <div>
                <strong>{member.email}{isSelf ? ' (вы)' : ''}</strong>
                <small>{memberStatusLabels[member.status] ?? member.status}</small>
                <span className={styles.roleChips}>
                  {member.roles.length === 0 && <small>без ролей</small>}
                  {member.roles.map((role) => <span key={role} className={styles.roleChip} data-role={role}>{roleLabels[role] ?? role}</span>)}
                </span>
              </div>
              <div>
                {/* The server refuses only a change of the administrator right itself; the other roles of an
                    administrator may be edited by a product owner, so the checklist locks just that box. */}
                {!isEditing && <button disabled={busy || member.status !== 'active'}
                  title={member.status !== 'active' ? 'Роли меняются после активации приглашения.' : undefined}
                  aria-label={`Изменить роли: ${member.email}`}
                  onClick={() => { setError(''); setEditing({ actorId: member.actor_id, roles: [...member.roles] }); }}>Изменить роли</button>}
                {revoking !== member.actor_id && <button disabled={busy || member.status === 'revoked' || isSelf || (holdsAdmin && !canGrantAdmin)}
                  title={isSelf ? 'Собственный доступ отозвать нельзя.' : holdsAdmin && !canGrantAdmin ? 'Доступ администратора отзывает только администратор.' : undefined}
                  aria-label={`Отозвать доступ: ${member.email}`}
                  onClick={() => setRevoking(member.actor_id)}>Отозвать доступ</button>}
              </div>
              {isEditing && editing && <div className={styles.editor} aria-label={`Редактор ролей: ${member.email}`}>
                <RoleChecklist legend={`Роли: ${member.email}`} name={`roles-${member.actor_id}`} selected={editing.roles} canGrantAdmin={canGrantAdmin}
                  onToggle={(role) => setEditing({ actorId: editing.actorId, roles: toggle(editing.roles, role) })} />
                <div className={styles.editorActions}>
                  <button disabled={busy || editing.roles.length === 0} onClick={() => void saveRoles()}>Сохранить роли</button>
                  <button disabled={busy} onClick={() => setEditing(null)}>Отмена</button>
                </div>
              </div>}
              {revoking === member.actor_id && <div className={styles.editor} role="group" aria-label={`Отзыв доступа: ${member.email}`}>
                <p>Отозвать доступ у {member.email}? Его веб-сессии и MCP-токены перестанут работать сразу.</p>
                <div className={styles.editorActions}>
                  <button disabled={busy} onClick={() => void revoke(member)}>Да, отозвать доступ</button>
                  <button disabled={busy} onClick={() => setRevoking('')}>Отмена</button>
                </div>
              </div>}
            </article>
          );
        })}
      </div>
      <h2>Журнал действий</h2>
      {audit.length === 0 ? <p>Записей пока нет.</p> : (
        <ol className={styles.audit}>
          {audit.map((item) => (
            <li key={item.id}>
              <time>{new Date(item.created_at).toLocaleString('ru-RU')}</time>
              <span>{item.actor_id}: {auditActionLabels[item.action] ?? item.action} → {item.target_actor_id}</span>
            </li>
          ))}
        </ol>
      )}
    </section>
  );
}
