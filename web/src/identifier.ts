// Идентификатор задачи/инициативы (feature). Регэксп повторяет featurePattern
// из pkg/workflow/identity.go: сервер принимает только такой slug.

export const identifierPattern = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

export const identifierHint = 'Латиница, цифры, «.», «_» и «-», до 64 символов, первый символ — буква или цифра. Например: google-auth';

const translit: Record<string, string> = {
  а: 'a', б: 'b', в: 'v', г: 'g', д: 'd', е: 'e', ё: 'yo', ж: 'zh', з: 'z', и: 'i', й: 'y',
  к: 'k', л: 'l', м: 'm', н: 'n', о: 'o', п: 'p', р: 'r', с: 's', т: 't', у: 'u', ф: 'f',
  х: 'kh', ц: 'ts', ч: 'ch', ш: 'sh', щ: 'shch', ъ: '', ы: 'y', ь: '', э: 'e', ю: 'yu', я: 'ya',
};

// Человеческое название -> черновик идентификатора: кириллица транслитерируется,
// остальное, кроме латиницы и цифр, превращается в дефис. Результат проходит
// identifierPattern либо пустой (тогда пользователь введёт вручную).
export function identifierFromTitle(title: string): string {
  const latin = Array.from(title.toLowerCase(), (ch) => translit[ch] ?? ch).join('');
  return latin
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 64)
    .replace(/-+$/, '');
}

// Пустая строка — значение допустимо. Иначе — текст, что именно неверно.
export function identifierError(value: string): string {
  if (value === '') return 'Укажите идентификатор.';
  const first = Array.from(value)[0];
  if (!/[A-Za-z0-9]/.test(first)) {
    return `Первый символ должен быть латинской буквой или цифрой, а не «${first}».`;
  }
  const invalid = /[^A-Za-z0-9._-]/.exec(value);
  if (invalid) {
    return `Недопустимый символ «${invalid[0]}». Используйте латиницу, цифры, «.», «_» или «-».`;
  }
  if (value.length > 64) {
    return `Слишком длинный идентификатор: ${value.length} символов, максимум 64.`;
  }
  return identifierPattern.test(value) ? '' : 'Идентификатор не соответствует формату.';
}

// Сервер отвечает на неверный feature общим текстом («feature и непустой task
// обязательны»), поэтому такую ошибку показываем у поля идентификатора.
export function isIdentifierServerError(message: string): boolean {
  return /feature|идентификатор/i.test(message);
}
