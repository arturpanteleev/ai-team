# Scheduler hardening (хвосты distributed-режима)

Статус: идея

## Задача

Закрыть известные шероховатости scheduler/worker: duplicate detection в
enqueue по строке ошибки `"unique"`; archive failure после успешного
исполнения делает job terminal failed без retry; worker Cancel исполняется с
`context.Background()` (неканцелируем); poller считает exit 1–3 «controlled»
магией кодов (частично заменено result-line контрактом).

## Ценность для продукта

Distributed-режим — витринная возможность для команд, и его слабые места
бьют именно там, где больнее всего: потерянный дубликат run'а, навсегда
«failed» работа при удачном исполнении (данные есть — статуса нет),
зависший cancel. Каждое из этих поведений сегодня требует ручного разбора
SQLite. Починка переводит scheduler из «работает у автора» в состояние,
которое можно предложить команде: предсказуемые статусы, честные retry,
наблюдаемые сбои.

## Рекомендации по реализации

- enqueue: ловить `sqlite3.ErrConstraintUnique` типизированно (или INSERT OR
  IGNORE + проверка rows);
- archiver: отдельная фаза со своим retry/backoff и статусом `archive_failed`
  вместо терминального failed;
- Cancel: контекст с таймаутом от родителя;
- дожать result-line контракт: убрать остаточные magic numbers из poller.

## Как проверить

1. Два параллельных enqueue одного run → один job, вторая попытка получает
   typed conflict.
2. Искусственный сбой CAS-архива: job в archive_failed, retry поднимает.
3. Отмена зависшего cancel-процесса по таймауту не блокирует poller.
