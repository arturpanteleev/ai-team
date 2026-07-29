# Проектирование

## Контекст

Stage projection уже содержит JSON-поля `checks`, `mutations` и `delivery`.
Agent runtime пишет отдельный лог каждого immutable attempt в
`.ai-team/runs/<run-id>/logs`, но web намеренно публикует только artifacts и
reports. Controller принимает start синхронно до запуска goroutine, поэтому
это последняя безопасная точка для fail-fast проверки окружения.

## Решение

Пакет `preflight` возвращает устойчивый к расширению report:

- каждая проверка имеет стабильный id, status, обязательность и безопасное
  сообщение без значений credentials;
- общий `ready` равен false только при failed required check;
- внешние команды выполняются с timeout и ограниченным объёмом вывода;
- наличие delivery определяется по скомпилированному pipeline registry;
- отсутствие явных credential env допускается как диагностика, поскольку
  OpenCode может использовать собственное локальное хранилище авторизации.

Controller выполняет тот же checker при `Preflight` и непосредственно перед
`Start`, исключая расхождение между показанным и реально применённым gate.
HTTP endpoint только отображает report и не становится источником истины.

## Live-логи

Read endpoint принимает exact `run_id` и `attempt_id`, проверяет их как один
безопасный path segment и читает только соответствующий immutable log.
Ответ ограничивается последними 64 KiB и сообщает смещение/признак
усечения. Frontend запрашивает лог только для раскрытого attempt и обновляет
его с коротким интервалом, пока attempt или run активен. Lifecycle и approvals
по-прежнему приходят через WebSocket; polling логов не заменяет event stream.

## Evidence UI

Frontend типизированно разбирает уже сохранённые `checks_json`,
`mutations_json` и `delivery_json`. Неизвестные дополнительные поля
сохраняются в raw JSON fallback, а повреждённая projection показывается как
ошибка данных вместо падения страницы.

## Не входит

- workflow graph и редактирование config;
- candidate worktree;
- cloud authentication/RBAC;
- передача полного stdout через WebSocket;
- хранение secrets или проверка их значений.
