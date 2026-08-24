# Context checkpoints & cross-run memory

Статус: idea
Источник: ALK parity — `docs/reference/context-checkpoints.md`,
`episode-retrieval.md`, `follow-up-register.md`

## Задача

Единый типизированный событийный лог сессии + чекпоинты, из которых любая
сессия может продолжиться. Не сырые логи на 10 000 строк (stdout агентов и
так лежит в evidence), а Event Log: что произошло, в каком порядке, с какими
решениями — плюс кросс-рановая память поверх этих событий.

## Ценность для продукта

Сегодня понять «что случилось в прогоне» можно только прочитав файлы
evidence вручную; контекст длинной сессии теряется при обрыве безвозвратно;
раны не знают друг о друге. Типизированный лог даёт три вещи: прозрачность
(человек и внешний агент видят историю решений в читаемом виде), надёжность
(любая пауза/обрыв продолжается с чекпоинта без потери plan authority) и
накопление опыта (поиск по прошлым ранам, регистр незакрытых хвостов между
задачами). Это же данные для notifiers, MCP-сервера и progress bridge —
три других плана потребляют именно этот лог.

## Рекомендации по реализации

### Конверт события

```json
{
  "v": 1,
  "seq": 42,
  "type": "approval.decided",
  "ts": "2026-08-23T10:15:30Z",
  "run_id": "...",
  "attempt_id": "... | null",
  "actor": "human:artur | controller | agent:reviewer",
  "data": {}
}
```

`seq` монотонный внутри run; `type` — из закрытого каталога
`домен.событие`; неизвестный тип при чтении отображается как opaque, не роняя
парсер. Append-only, источник истины — immutable evidence (`events.jsonl`);
SQLite/WebSocket остаются projections.

### Каталог типов

Жизненный цикл run: `run.started`, `run.resumed`, `run.paused`,
`run.canceled`, `run.finished`.

Исполнение этапов: `attempt.started`, `attempt.finished`,
`attempts.invalidated`, `attempt.abandoned`, `transition.selected`.

Решения человека: `approval.requested`, `approval.decided`,
`delivery.approved`.

Контроллер-эффекты: `check.executed`, `guard.violated`, `delivery.step`,
`checkpoint.taken` (новое).

Контекст и память (новые): `context.compacted`, `followup.opened`,
`followup.closed`.

У каждого типа фиксированный payload (stage, outcome, verdict?, approval_id,
subject_hash, actor, reason...) — полные таблицы при реализации.

### Классы реакции (контракт для потребителей)

- `RESUME_POINT`: paused / attempt.finished / checkpoint.taken — отсюда
  можно продолжить;
- `NEEDS_HUMAN`: approval.requested — единственный класс, требующий действия;
- `INVALIDATING`: attempts.invalidated, guard.violated — downstream
  помечается superseded;
- `TERMINAL`: run.finished / run.canceled;
- остальное — `INFO`.

Инвариант replay'я: нет второго started без finished того же stage; после
TERMINAL новых событий нет.

### Read model (как вычитывать)

1. **Timeline** (`ai-team events --run <id> [--type ...]`, web detail) —
   лента с человекочитаемыми подписями из каталога типов.
2. **Pending queue** — открытые approval.requested по всем ранам.
3. **Checkpoint restore** — bounded continuation-пакет по `checkpoint.taken`;
   resume с любого чекпоинта, не только между этапами.
4. **Cross-run search** — поиск по data событий за все раны; основа
   follow-up регистра и памяти.

Миграция: сегодняшние snake_case события маппятся на каталог один-к-одному —
это переименование + добавление `actor` и класса реакции, не новая система.
Ориентиры у ALK: context-checkpoints, episode-retrieval, follow-up register.

## Как проверить

1. Timeline-команда показывает весь прогон событиями каталога; незнакомый
   тип не ломает вывод.
2. Убийство процесса в произвольной точке → resume с ближайшего
   RESUME_POINT завершает тот же run корректно (существующие E2E +
   новый сценарий чекпоинта внутри этапа).
3. Поиск по двум завершённым ранам находит события по фильтрам типа/этапа;
   follow-up, открытый в одном ране, виден и закрывается в другом.
