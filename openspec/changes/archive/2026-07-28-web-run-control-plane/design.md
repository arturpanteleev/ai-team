# Проектирование

## Контекст

`Pipeline` уже умеет start/resume одного persisted run, а approval store
принимает exact decisions. HTTP-сервер владеет SQLite projection и
WebSocket-tail, но не процессами выполнения.

## Решение

Вводится process-local `RunController`, который:

- резервирует run ID до запуска goroutine;
- не допускает двух активных workers одного run;
- хранит cancel functions только для активных процессов;
- запускает один и тот же `RunEngine` для CLI-совместимой семантики;
- после cancel доводит paused lifecycle до terminal canceled;
- пишет approval decision напрямую в общий atomic store.

HTTP handler возвращает `202 Accepted` для start/resume/cancel, поскольку
результат исполнения приходит через persisted projection и WebSocket.
Ошибки схемы команды и конфликты active run возвращаются синхронно.

## Session и CSRF

При старте server генерирует независимые криптографические session и CSRF
tokens. `GET /api/session` устанавливает HttpOnly SameSite=Strict cookie и
возвращает CSRF token same-origin frontend-у. Каждый write request обязан
прислать cookie и `X-CSRF-Token`; сравнение выполняется constant-time.
Существующая fixed loopback Host/Origin allow-list остаётся первым барьером.

Это локальная защита, не cloud authentication/RBAC. Полноценная identity
появится отдельной фичей; сейчас actor передаётся явно и неизменно попадает в
approval audit.

## Отказоустойчивость

Controller не является источником истины. После restart active goroutine
теряется, но lifecycle/evidence остаются resumable. Browser вызывает resume,
а SQLite/WebSocket восстанавливаются из persisted событий.

## Не входит

- редактирование workflow config;
- cloud authentication и RBAC;
- распределённые workers;
- runtime preflight и расширенная observability.
