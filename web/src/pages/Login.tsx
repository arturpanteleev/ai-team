import { useState } from 'react';
import type { FormEvent } from 'react';
import styles from './Login.module.css';

export function Login({ onLogin, onActivate }: { onLogin: (token: string) => Promise<void>; onActivate: (token: string) => Promise<void> }) {
  const [token, setToken] = useState('');
  const [inviteToken, setInviteToken] = useState('');
  const [error, setError] = useState('');
  const [pending, setPending] = useState(false);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setPending(true);
    setError('');
    try {
      await onLogin(token.trim());
    } catch (value) {
      setError(value instanceof Error ? value.message : 'Authentication failed');
    } finally {
      setPending(false);
    }
  };

  const activate = async () => {
    setPending(true); setError('');
    try { await onActivate(inviteToken.trim()); }
    catch (value) { setError(value instanceof Error ? value.message : 'Invitation activation failed'); }
    finally { setPending(false); }
  };

  return (
    <main className={styles.page}>
      <form className={styles.card} onSubmit={submit}>
        <h1>ai-team cloud</h1>
        <p>Сессия истекла или отсутствует. Войдите снова с короткоживущим access token от control plane.</p>
        <textarea aria-label="Access token" value={token}
          onChange={(event) => setToken(event.target.value)} autoFocus />
        {error && <div className={styles.error}>{error}</div>}
        <button disabled={pending || token.trim() === ''}>
          {pending ? 'Проверка…' : 'Войти'}
        </button>
      </form>
      <section className={styles.card}>
        <h2>Есть код приглашения?</h2>
        <p>Активируйте приглашение, чтобы получить access token для этой команды.</p>
        <textarea aria-label="Invitation code" value={inviteToken} onChange={event => setInviteToken(event.target.value)} />
        {error && <div className={styles.error}>{error}</div>}
        <button type="button" disabled={pending || inviteToken.trim() === ''} onClick={() => void activate()}>
          {pending ? 'Активация…' : 'Активировать приглашение'}
        </button>
      </section>
    </main>
  );
}
