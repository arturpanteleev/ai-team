// Единый словарь действий. Одно смысловое действие — одна подпись на кнопке,
// в маршруте («Доступно: …»), в журнале и в документации MCP. Новые подписи
// заводятся здесь, а не по месту.

export const actionVerbs = {
  submitResult: 'Сдать результат',
  takeStage: 'Взять этап',
  runAgent: 'Поручить агенту',
  refineWithAgent: 'Доработать агентом',
  changeExecutor: 'Сменить исполнителя',
  accept: 'Принять и перейти',
  acceptFinal: 'Принять и завершить задачу',
  acceptPlan: 'Принять план доставки',
  reject: 'Отклонить',
  skip: 'Пропустить этап',
  returnStage: 'Вернуть на доработку',
  overrideCheck: 'Обойти проверку',
  retryGate: 'Повторить гейт',
  answerAndContinue: 'Ответить и продолжить',
  newVersion: 'Отправить новую версию',
  finishGrill: 'Завершить грилл без ответа',
  resumeTask: 'Продолжить задачу',
  resumeOnCurrentVersion: 'Продолжить на текущей версии',
  grantBudget: 'Выдать бюджет и продолжить',
  cancelTask: 'Отменить задачу',
  retryDelivery: 'Повторить Git-доставку',
  openLog: 'Открыть лог',
  linkTracker: 'Связать с карточкой',
  unlinkTracker: 'Снять связь',
  retryTracker: 'Повторить отправку в трекер',
  syncTracker: 'Синхронизировать сейчас',
  dismissInbound: 'Закрыть запрос без действий',
  newTask: 'Новая задача',
  createTask: 'Создать задачу',
} as const;

// Имена действий API (allowed_actions) в тех же словах.
export const allowedActionLabels: Record<string, string> = {
  take: actionVerbs.takeStage,
  results: actionVerbs.submitResult,
  'run-agent': actionVerbs.runAgent,
  executor: actionVerbs.changeExecutor,
  confirm: 'Принять и перейти',
  override: actionVerbs.overrideCheck,
  'gate-retry': actionVerbs.retryGate,
  skip: actionVerbs.skip,
  return: actionVerbs.returnStage,
  'grill/answer': actionVerbs.answerAndContinue,
  'grill/new-version': actionVerbs.newVersion,
  'grill/done': actionVerbs.finishGrill,
  'plan/approve': actionVerbs.acceptPlan,
};

export function allowedActionText(action: string): string {
  return allowedActionLabels[action] ?? action;
}
