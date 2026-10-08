import { useState } from 'react';
import { shortenHash } from '../hash';
import styles from './CopyableHash.module.css';

export function CopyableHash({ value, label = 'хеш' }: { value: string; label?: string }) {
  const [feedback, setFeedback] = useState('');

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setFeedback('Скопировано');
    } catch {
      setFeedback('Не удалось скопировать');
    }
  };

  return (
    <span className={styles.container}>
      <code className={styles.hash} title={value} aria-label={`${label}: ${value}`}>
        {shortenHash(value)}
      </code>
      <button type="button" className={styles.copy} onClick={() => void copy()} aria-label={`Скопировать ${label}`}>
        Копировать
      </button>
      {feedback && <span className={styles.copied} role="status">{feedback}</span>}
    </span>
  );
}
