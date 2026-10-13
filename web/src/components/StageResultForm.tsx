import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { ChangeEvent, FormEvent } from 'react';
import type { TaskStageTemplate } from '../types';
import {
  clearStageResultDraft, emptyStageResultDraft, isEmptyStageResultDraft, loadStageResultDraft, saveStageResultDraft,
} from '../stageResultDraft';
import type { StageResultDraft } from '../stageResultDraft';
import { MarkdownPreview } from './MarkdownPreview';
import { MAX_MARKDOWN_BYTES, MAX_TEXT_BYTES, formatBytes, linkError, linkHint, linkKindTitles, utf8Length } from '../stageResultValidation';
import type { StageResultSubmission } from '../stageResultValidation';
import styles from './StageResultForm.module.css';

// Draft writes serialise the whole form, which is slow for multi-megabyte
// Markdown; they are batched while the person types and flushed when the page
// is hidden, the form closes or the result is sent.
export const DRAFT_SAVE_DELAY_MS = 400;

async function readMarkdownFile(file: File): Promise<string> {
  if (file.size > MAX_MARKDOWN_BYTES) {
    throw new Error(`Файл ${file.name} больше ${formatBytes(MAX_MARKDOWN_BYTES)} (${formatBytes(file.size)}).`);
  }
  const bytes = await new Promise<ArrayBuffer>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as ArrayBuffer);
    reader.onerror = () => reject(new Error(`Не удалось прочитать файл ${file.name}.`));
    reader.readAsArrayBuffer(file);
  });
  try {
    return new TextDecoder('utf-8', { fatal: true }).decode(new Uint8Array(bytes));
  } catch {
    throw new Error(`Файл ${file.name} должен быть текстом в UTF-8.`);
  }
}

export function StageResultForm({ runId, stage, working, onSubmit, onCancel }: {
  runId: string;
  stage: TaskStageTemplate;
  working: boolean;
  onSubmit: (value: StageResultSubmission) => void;
  onCancel: () => void;
}) {
  const [initial] = useState(() => loadStageResultDraft(runId, stage.id));
  const [draft, setDraft] = useState(initial ?? emptyStageResultDraft);
  const [restored, setRestored] = useState(initial !== null);
  const [persisted, setPersisted] = useState(true);
  const [preview, setPreview] = useState(false);
  const [fileError, setFileError] = useState('');
  const [fileName, setFileName] = useState('');

  const pendingDraft = useRef<StageResultDraft | null>(null);
  const saveTimer = useRef<number | undefined>(undefined);
  // Set once the text was handed to onSubmit: the caller clears the stored
  // draft after the server accepts it, so neither a pending debounced write nor
  // the final write on close may bring it back. New input resets it.
  const submitted = useRef(false);
  const flushDraft = useCallback(() => {
    window.clearTimeout(saveTimer.current);
    saveTimer.current = undefined;
    const value = pendingDraft.current;
    pendingDraft.current = null;
    if (!value || submitted.current) return;
    setPersisted(saveStageResultDraft(runId, stage.id, value));
  }, [runId, stage.id]);

  // The draft on mount is already what storage holds (or nothing); only edits
  // are scheduled for writing.
  const loadedDraft = useRef(true);
  useEffect(() => {
    if (loadedDraft.current) {
      loadedDraft.current = false;
      return;
    }
    pendingDraft.current = draft;
    window.clearTimeout(saveTimer.current);
    saveTimer.current = window.setTimeout(flushDraft, DRAFT_SAVE_DELAY_MS);
  }, [draft, flushDraft]);

  useEffect(() => {
    window.addEventListener('pagehide', flushDraft);
    return () => {
      window.removeEventListener('pagehide', flushDraft);
      flushDraft();
    submitted.current = true;
    };
  }, [flushDraft]);

  const update = (field: keyof typeof draft) => (event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
    const value = event.target.value;
    submitted.current = false;
    setDraft((current) => ({ ...current, [field]: value }));
  };

  const contentBytes = useMemo(() => utf8Length(draft.content), [draft.content]);
  const kind = stage.result;
  const errors: string[] = [];
  if (kind === 'md' && contentBytes > MAX_MARKDOWN_BYTES) {
    errors.push(`Результат больше ${formatBytes(MAX_MARKDOWN_BYTES)} (${formatBytes(contentBytes)}).`);
  }
  const urlError = kind === 'link' ? linkError(draft.url) : '';
  if (urlError) errors.push(urlError);
  if (kind === 'link' && !stage.link_kind) errors.push('В шаблоне этапа не задан вид ссылки — сдать ссылку нельзя.');
  if (utf8Length(draft.description) > MAX_TEXT_BYTES) errors.push(`«Что сделано» длиннее ${formatBytes(MAX_TEXT_BYTES)}.`);
  if (utf8Length(draft.note) > MAX_TEXT_BYTES) errors.push(`${kind === 'approve' ? 'Текст подтверждения' : 'Заметка'} длиннее ${formatBytes(MAX_TEXT_BYTES)}.`);
  const missingValue = kind === 'md' && !draft.content.trim() || kind === 'link' && !draft.url.trim();
  const descriptionMissing = !draft.description.trim();

  const chooseFile = async (event: ChangeEvent<HTMLInputElement>) => {
    const input = event.currentTarget;
    const file = input.files?.[0];
    if (!file) return;
    try {
      const text = await readMarkdownFile(file);
      submitted.current = false;
      setDraft((current) => ({ ...current, content: text }));
      setFileName(file.name);
      setFileError('');
    } catch (reason) {
      setFileError(reason instanceof Error ? reason.message : 'Не удалось прочитать файл.');
    } finally {
      input.value = '';
    }
  };

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (working || missingValue || errors.length > 0) return;
    // Persist the exact submitted text first: a rejected submission keeps it,
    // an accepted one is cleared by the caller without a late write behind it.
    flushDraft();
    submitted.current = true;
    const note = draft.note.trim();
    const description = draft.description.trim();
    onSubmit({
      result: kind,
      ...(kind === 'md' ? { content: draft.content } : {}),
      ...(kind === 'link' && stage.link_kind ? { url: draft.url.trim(), link_kind: stage.link_kind } : {}),
      ...(note ? { note } : {}),
      ...(description ? { description } : {}),
    });
  };

  const discard = () => {
    if (!isEmptyStageResultDraft(draft) &&
      !window.confirm('Очистить черновик? Введённый текст будет удалён из формы и из этого браузера.')) return;
    window.clearTimeout(saveTimer.current);
    pendingDraft.current = null;
    clearStageResultDraft(runId, stage.id);
    setDraft(emptyStageResultDraft);
    setRestored(false);
    setFileName('');
    setFileError('');
  };

  return (
    <form className={styles.form} onSubmit={submit} noValidate>
      <h3>Результат этапа «{stage.title}»</h3>
      {restored && <p className={styles.info} role="status">Восстановлен черновик, сохранённый в этом браузере.</p>}
      {!persisted && <p className={styles.warning} role="status">
        Черновик не удаётся сохранить в браузере: не закрывайте страницу до отправки.
      </p>}

      {kind === 'md' && <section className={styles.editor} aria-label="Редактор Markdown">
        <div className={styles.toolbar}>
          <button type="button" aria-pressed={!preview} onClick={() => setPreview(false)}>Редактор</button>
          <button type="button" aria-pressed={preview} onClick={() => setPreview(true)}>Предпросмотр</button>
          <label className={styles.upload}>Загрузить файл .md
            <input type="file" accept=".md,.markdown,.txt,text/markdown,text/plain" disabled={working}
              onChange={(event) => void chooseFile(event)} />
          </label>
          <span className={styles.size}>{formatBytes(contentBytes)} из {formatBytes(MAX_MARKDOWN_BYTES)}</span>
        </div>
        {fileName && !fileError && <p className={styles.info}>Загружен файл: {fileName}</p>}
        {fileError && <p className={styles.error} role="alert">{fileError}</p>}
        {preview
          ? <MarkdownPreview className={styles.preview} label="Предпросмотр результата" markdown={draft.content}
            emptyText="Пока пусто." />
          : <label>Результат в Markdown
            <textarea value={draft.content} onChange={update('content')} rows={14} disabled={working} />
          </label>}
      </section>}

      {kind === 'link' && <>
        <label>Ссылка на результат
          <input type="url" inputMode="url" value={draft.url} onChange={update('url')} placeholder="https://" disabled={working}
            aria-invalid={urlError ? true : undefined} />
        </label>
        <p className={styles.info}>Вид ссылки: {stage.link_kind ? linkKindTitles[stage.link_kind] : 'не задан'}</p>
        {linkHint(draft.url, stage.link_kind) && <p className={styles.warning}>{linkHint(draft.url, stage.link_kind)}</p>}
      </>}

      {kind === 'approve' && <p>Подтвердите завершение этапа. Текст подтверждения необязателен.</p>}

      <label>Что сделано
        <textarea value={draft.description} onChange={update('description')} rows={3} disabled={working} />
      </label>
      {descriptionMissing && <p className={styles.warning}>
        Поле «Что сделано» пустое. Результат можно сдать, но в задаче появится предупреждение «Нет описания результата».
      </p>}
      <label>{kind === 'approve' ? 'Текст подтверждения' : 'Заметка'}
        <textarea value={draft.note} onChange={update('note')} rows={3} disabled={working} />
      </label>

      {errors.length > 0 && <ul className={styles.error} role="alert">{errors.map((error) => <li key={error}>{error}</li>)}</ul>}

      <div className={styles.actions}>
        <button disabled={working || missingValue || errors.length > 0}>
          {working ? 'Отправляем…' : kind === 'approve' ? 'Подтвердить результат' : 'Отправить результат'}
        </button>
        <button type="button" disabled={working} onClick={onCancel}>Свернуть</button>
        <button type="button" disabled={working} onClick={discard}>Очистить черновик</button>
      </div>
    </form>
  );
}
