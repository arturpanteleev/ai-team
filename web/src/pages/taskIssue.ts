// Turns the raw failure text of a stage or a resume into one short, actionable
// explanation. The raw text stays available under «Подробности».
export type IssueCode = 'version' | 'auth' | 'agent' | 'other';

export interface IssueClass {
  code: IssueCode;
  explanation: string;
}

export function classifyIssue(text: string): IssueClass {
  const value = text.toLowerCase();
  if (value.includes('provenance drift')) {
    return {
      code: 'version',
      explanation: 'Сервер обновлён после создания задачи. Можно продолжить на текущей версии: смена версии запишется в журнал задачи.',
    };
  }
  if (value.includes('claude-authentication') || value.includes('not logged in') || value.includes('аутентификац')) {
    return {
      code: 'auth',
      explanation: 'Агент не смог войти в Claude. Проверьте, что сервер запущен с рабочим входом, и запустите агента заново кнопкой ниже.',
    };
  }
  if (value.includes('агент') || value.includes('claude') || value.includes('infra_failed') || value.includes('exit status')) {
    return {
      code: 'agent',
      explanation: 'Агент завершился с ошибкой до получения результата. Запустите агента заново кнопкой ниже. Если ошибка повторяется, откройте подробности.',
    };
  }
  return {
    code: 'other',
    explanation: 'Этап остановился. Откройте подробности, чтобы увидеть причину.',
  };
}
