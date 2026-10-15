import { useEffect, useState } from 'react';
import { issueMcpToken, listMcpTokens, revokeMcpToken } from '../api';
import type { McpToken } from '../api';
import { usePageTitle } from '../hooks/usePageTitle';
import styles from './Team.module.css';

function tokenState(token: McpToken): string {
  if (token.revoked_at) return 'отозван';
  if (new Date(token.expires_at).getTime() <= Date.now()) return 'истёк';
  return 'действует';
}

export function McpAccess() {
  usePageTitle('Доступ MCP');
  const [tokens, setTokens] = useState<McpToken[]>([]);
  const [label, setLabel] = useState('');
  const [ttlDays, setTtlDays] = useState(90);
  const [secret, setSecret] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const endpoint = `${window.location.origin}/mcp`;

  const refresh = async () => setTokens((await listMcpTokens()).tokens);

  useEffect(() => {
    void refresh().catch((value: unknown) => setError(value instanceof Error ? value.message : 'Не удалось загрузить токены.'));
  }, []);

  const create = async (event: React.FormEvent) => {
    event.preventDefault();
    setBusy(true);
    setError('');
    setSecret('');
    try {
      const issued = await issueMcpToken(label, ttlDays);
      setSecret(issued.token);
      setLabel('');
      await refresh();
    } catch (value) {
      setError(value instanceof Error ? value.message : 'Не удалось выпустить токен.');
    } finally {
      setBusy(false);
    }
  };

  const revoke = async (token: McpToken) => {
    if (!window.confirm(`Отозвать токен «${token.label}»?`)) return;
    setBusy(true);
    setError('');
    try {
      await revokeMcpToken(token.id);
      await refresh();
    } catch (value) {
      setError(value instanceof Error ? value.message : 'Не удалось отозвать токен.');
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className={styles.page}>
      <h1>Доступ MCP</h1>
      <p>
        Личный токен подключает Claude или Codex к ai-team по адресу <code>{endpoint}</code>. Агент действует от вашего имени
        с вашими ролями и видит только то, что доступно вам. Токен можно выпустить только для себя; при отзыве доступа к команде он перестаёт работать сразу.
      </p>
      {error && <p role="alert" className={styles.error}>{error}</p>}
      <form onSubmit={create} className={styles.invite}>
        <h2>Выпустить токен</h2>
        <label htmlFor="mcp-label">Название (где будет использоваться)
          <input id="mcp-label" type="text" required maxLength={80} value={label} onChange={(event) => setLabel(event.target.value)} />
        </label>
        <label htmlFor="mcp-ttl">Срок действия, дней
          <input id="mcp-ttl" type="number" min={1} max={365} required value={ttlDays} onChange={(event) => setTtlDays(Number(event.target.value))} />
        </label>
        <button disabled={busy || label.trim() === ''}>{busy ? 'Сохраняем…' : 'Выпустить токен'}</button>
      </form>
      {secret && (
        <div className={styles.invitation}>
          <strong>Скопируйте токен сейчас — повторно он не показывается:</strong>
          <code>{secret}</code>
          <small>Передайте его клиенту как заголовок Authorization: Bearer … (см. docs/reference/mcp.md).</small>
        </div>
      )}
      <h2>Мои токены</h2>
      <div className={styles.members}>
        {tokens.length === 0 && <p>Токенов пока нет.</p>}
        {tokens.map((token) => (
          <article key={token.id}>
            <div>
              <strong>{token.label}</strong>
              <small>
                …{token.hint} · {tokenState(token)} · до {new Date(token.expires_at).toLocaleDateString('ru-RU')}
                {token.last_used_at ? ` · использован ${new Date(token.last_used_at).toLocaleString('ru-RU')}` : ' · не использовался'}
              </small>
            </div>
            <div>
              <button disabled={busy || token.revoked_at !== undefined} onClick={() => void revoke(token)}>Отозвать</button>
            </div>
          </article>
        ))}
      </div>
    </section>
  );
}
