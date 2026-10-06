import { useEffect, useState } from 'react';
import { getTeamAudit, getTeamMembers, inviteTeamMember, revokeTeamMember, setTeamMemberRoles } from '../api';
import type { TeamAuditEvent, TeamMember } from '../api';
import styles from './Team.module.css';

const roles = ['product_owner', 'architect', 'developer', 'reviewer', 'qa', 'release_manager'];

export function Team() {
  const [members,setMembers]=useState<TeamMember[]>([]);
  const [audit,setAudit]=useState<TeamAuditEvent[]>([]);
  const [email,setEmail]=useState('');
  const [selected,setSelected]=useState<string[]>(['product_owner']);
  const [invite,setInvite]=useState('');
  const [error,setError]=useState('');
  const [busy,setBusy]=useState(false);
  const refresh=async()=>{const [m,a]=await Promise.all([getTeamMembers(),getTeamAudit()]);setMembers(m.members);setAudit(a.events);};
  useEffect(()=>{void refresh().catch(value=>setError(value instanceof Error?value.message:'Не удалось загрузить команду'));},[]);
  const toggle=(role:string)=>setSelected(current=>current.includes(role)?current.filter(item=>item!==role):[...current,role]);
  const createInvite=async(event:React.FormEvent)=>{event.preventDefault();setBusy(true);setError('');setInvite('');try{const value=await inviteTeamMember(email,selected);setInvite(value.activation_token);setEmail('');await refresh();}catch(value){setError(value instanceof Error?value.message:'Не удалось создать приглашение');}finally{setBusy(false);}};
  const editRoles=async(member:TeamMember)=>{const input=window.prompt(`Роли для ${member.email}, через запятую`,member.roles.join(', '));if(input===null)return;const nextRoles=input.split(',').map(v=>v.trim()).filter(Boolean);if(nextRoles.length===0){setError('Укажите хотя бы одну роль.');return;}setBusy(true);setError('');try{await setTeamMemberRoles(member.actor_id,nextRoles);await refresh();}catch(value){setError(value instanceof Error?value.message:'Не удалось изменить роли');}finally{setBusy(false);}};
  const revoke=async(member:TeamMember)=>{if(!window.confirm(`Отозвать доступ ${member.email}?`))return;setBusy(true);setError('');try{await revokeTeamMember(member.actor_id);await refresh();}catch(value){setError(value instanceof Error?value.message:'Не удалось отозвать доступ');}finally{setBusy(false);}};
  return <section className={styles.page}>
    <h1>Команда</h1><p>Участники одного рабочего пространства. Изменение ролей и отзыв доступа завершают действующие web-сессии.</p>
    {error&&<p role="alert" className={styles.error}>{error}</p>}
    <form onSubmit={createInvite} className={styles.invite}>
      <h2>Пригласить участника</h2><label>Email<input type="email" required value={email} onChange={event=>setEmail(event.target.value)}/></label>
      <fieldset><legend>Роли</legend>{roles.map(role=><label key={role}><input type="checkbox" checked={selected.includes(role)} onChange={()=>toggle(role)}/>{role}</label>)}</fieldset>
      <button disabled={busy||selected.length===0}>Создать приглашение</button>
    </form>
    {invite&&<div className={styles.invitation}><strong>Передайте этот одноразовый код участнику:</strong><code>{invite}</code><small>Срок действия — 48 часов. Код показывается только сейчас.</small></div>}
    <h2>Участники</h2><div className={styles.members}>{members.map(member=><article key={member.actor_id}>
      <div><strong>{member.email}</strong><small>{member.status} · {member.roles.join(', ')||'без ролей'}</small></div>
      <div><button disabled={busy||member.actor_id===''} onClick={()=>void editRoles(member)}>Изменить роли</button><button disabled={busy||member.status==='revoked'} onClick={()=>void revoke(member)}>Отозвать</button></div>
    </article>)}</div>
    <h2>Журнал действий</h2><ol className={styles.audit}>{audit.map(item=><li key={item.id}><time>{new Date(item.created_at).toLocaleString()}</time><span>{item.actor_id}: {item.action} → {item.target_actor_id}</span></li>)}</ol>
  </section>;
}
