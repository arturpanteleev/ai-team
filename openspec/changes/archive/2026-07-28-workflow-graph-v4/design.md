# Проектирование

## Модель

Schema v4 сохраняет `pipeline` как определения узлов и добавляет:

```yaml
workflow:
  entry: analyst
  max_visits:
    coder: 3
    reviewer: 3
  edges:
    - from: analyst
      outcome: passed
      to: architect
      approval:
        roles: [product_owner]
        quorum: any
        actions:
          approve: architect
          reject: $stop
```

Terminal targets: `$complete`, `$failed`, `$blocked`, `$stop`. Action target
может отличаться от основного `to`, что сохраняет `override_approve` без
hard-coded reviewer logic.

## Валидация

Compiler строит immutable graph и до создания run проверяет:

- exact entry/node references и уникальность пары `(from, outcome)`;
- допустимые outcomes и terminal targets;
- достижимость всех узлов и наличие пути к terminal;
- approval на каждом non-terminal edge schema v4;
- roles/quorum/actions и exact targets approval;
- положительный `max_visits` для каждого узла, входящего в цикл.

Для schemas 1–3 compiler строит линейные `passed` edges и совместимый
`rejected` loopback из существующих полей. Эта компиляция не переписывает
пользовательский файл и сохраняет прежние checkpoints.

## Исполнение

Engine ведёт текущий node и visit counter вместо изменения индекса цикла.
После attempt он записывает outcome, выбирает ровно одно ребро и, если policy
присутствует, создаёт exact human approval. Выбранный action определяет exact
target. Обратное ребро инвалидирует downstream attempts и передаёт immutable
output отклонившего этапа целевому узлу.

Visit counters восстанавливаются из immutable attempts при resume. Превышение
лимита завершает run до следующего AI call. Lifecycle `next_stage` всегда
равен выбранной цели.

## Web

Resolved workflow snapshot получает собственную schema и compiled graph.
Read endpoint отдаёт только immutable `workflow.json` конкретного run.
Detail page рисует компактный список узлов/рёбер, policy и текущий узел;
редактор графа в scope не входит.

## Не входит

- параллельное исполнение нескольких узлов;
- визуальный редактор;
- candidate worktree;
- cloud scheduler и distributed locks.
