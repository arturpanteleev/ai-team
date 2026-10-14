import { describe, expect, it } from 'vitest';
import { taskCardTitle } from './boardTitle';

describe('taskCardTitle', () => {
  it('берёт первую строку идеи, пропуская заголовок «# …»', () => {
    expect(taskCardTitle('# Бриф\n\nСервис рассылок для клиентов\nПодробности', 'Шаблон'))
      .toBe('Сервис рассылок для клиентов');
  });

  it('снимает маркеры списка и жирное выделение', () => {
    expect(taskCardTitle('- **Быстрый отчёт** по кампаниям', undefined)).toBe('Быстрый отчёт по кампаниям');
  });

  it('обрезает длинную идею до 80 символов с многоточием', () => {
    const title = taskCardTitle('а'.repeat(200), undefined);
    expect([...title]).toHaveLength(80);
    expect(title.endsWith('…')).toBe(true);
  });

  it('без идеи использует название шаблона', () => {
    expect(taskCardTitle('# Только заголовок', 'Стандартный маршрут')).toBe('Стандартный маршрут');
    expect(taskCardTitle(undefined, '  Стандартный маршрут ')).toBe('Стандартный маршрут');
  });

  it('без идеи и шаблона не показывает идентификатор', () => {
    expect(taskCardTitle('', undefined)).toBe('Задача без описания');
  });
});
