const MAX_TITLE_LENGTH = 80;

/**
 * Человеческий заголовок карточки доски: первая содержательная строка бизнес-идеи
 * (строки-заголовки «# …» пропускаются), иначе название шаблона.
 * Идентификатор задачи сюда не попадает — он показывается отдельно и мелко.
 */
export function taskCardTitle(brief: string | undefined, templateTitle: string | undefined): string {
  const idea = firstIdeaLine(brief);
  if (idea) return truncate(idea, MAX_TITLE_LENGTH);
  const template = templateTitle?.trim();
  return template ? truncate(template, MAX_TITLE_LENGTH) : 'Задача без описания';
}

export function firstIdeaLine(brief: string | undefined): string {
  for (const raw of (brief ?? '').split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || /^#{1,6}\s/.test(line)) continue;
    const text = line.replace(/^(?:[-*+>]\s+)+/, '').replace(/\*\*/g, '').trim();
    if (text) return text;
  }
  return '';
}

function truncate(text: string, max: number): string {
  const chars = [...text];
  return chars.length > max ? `${chars.slice(0, max - 1).join('').trimEnd()}…` : text;
}
