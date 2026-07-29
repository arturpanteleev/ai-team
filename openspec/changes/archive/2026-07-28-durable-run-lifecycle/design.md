# Проектирование

## Persisted state

Mutable controller state хранится отдельно от immutable evidence:
`.ai-team/state/runs/<run_id>.json`. Запись выполняется через temporary file,
`fsync`, atomic rename. Документ содержит schema version, run_id, feature,
target, phase (`running`, `waiting`, `resumable`, `terminal`), next stage,
attempt ordinal, timestamps и hash config/workflow snapshots.

SQLite остаётся web projection, а immutable `.ai-team/runs/<run_id>/` —
аудитным evidence. Mutable state является checkpoint для исполнения, но его
identity проверяется по evidence перед resume.

## RunEngine

`RunEngine.Start` создаёт run, evidence и lifecycle state. `RunEngine.Resume`
под exclusive workspace lock загружает state, проверяет event chain и
snapshots, открывает evidence store для append и продолжает с сохранённого
этапа. Непосредственная логика стадии остаётся в pipeline; engine управляет
границами сессии и checkpoint.

## Evidence append

`evidence.Resume` читает `run.json`, проверяет bounded event log и восстанавливает
последние sequence/hash. Manifest и snapshots не перезаписываются. Resume
добавляет `run_resumed` с session timestamp. Попытка продолжить terminal run
отклоняется.

## Crash и штатная пауза

Crash оставляет state в `running`; следующий resume переводит его в
`resumable` после reconciliation незавершённой попытки. Штатная будущая
approval-пауза будет записывать `waiting` и не завершать run. Текущий change
не вводит роли или решения человека, но предоставляет безопасную точку
продолжения для следующего change.

## Совместимость

`--retry-from` без `--resume` сохраняется как новый run на основе live
projection. С `--resume` нельзя передавать новый task или менять feature.
Старые runs без lifecycle state доступны для просмотра, но автоматически не
возобновляются.
