import { useState } from 'react';
import type { FormEvent } from 'react';
import styles from './Login.module.css';

export function Login({ onLogin }: { onLogin: (token: string) => Promise<void> }) {
  const [token, setToken] = useState('');
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

  return (
    <main className={styles.page}>
      <form className={styles.card} onSubmit={submit}>
        <h1>ai-team cloud</h1>
        <p>Введите короткоживущий access token, выданный control plane.</p>
        <textarea aria-label="Access token" value={token}
          onChange={(event) => setToken(event.target.value)} autoFocus />
        {error && <div className={styles.error}>{error}</div>}
        <button disabled={pending || token.trim() === ''}>
          {pending ? 'Проверка…' : 'Войти'}
        </button>
      </form>
    </main>
  );
}
