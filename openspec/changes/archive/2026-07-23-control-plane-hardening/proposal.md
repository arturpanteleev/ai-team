## Why

Текущий ai-team уже умеет останавливать pipeline по части негативных вердиктов,
писать отчёты и историю запусков, но сохраняет false-green сценарии: отсутствующий
verdict считается успехом, старые артефакты могут быть приняты за новые, отчёты
расходятся с exit code, а deployer выполняет внешние операции без проверяемого
результата. Из-за этого заявленные гарантии детерминизма, контроля и
наблюдаемости пока не выполняются end-to-end.

Нужен единый control plane, в котором LLM создаёт предложения и смысловые
артефакты, а код контроллера владеет состояниями, проверками, разрешениями,
артефактами, retry и внешними побочными эффектами.

## What Changes

1. Verdict-контракт становится обязательным и fail-closed для verdict-bearing
   ролей; неоднозначные, отсутствующие и неизвестные verdict считаются ошибкой.
2. Каждый run и attempt получает идентификатор, immutable evidence и manifest с
   hash/provenance; старые выходы не могут быть засчитаны повторно.
3. Execution state, semantic decision и итоговый outcome разделяются; console,
   HTML, SQLite и exit code строятся из одной модели.
4. Checkpoints получают явную non-interactive policy; required approval не может
   быть автоматически пропущен.
5. Проверки build/test/lint/security выполняются детерминированным command runner,
   а не принимаются только из текста LLM-отчёта.
6. Delivery отделяется от agent stage: агент готовит план, контроллер валидирует и
   выполняет разрешённые git/GitHub-операции идемпотентно.
7. OpenSpec strict-validation, traceability и evidence становятся обязательными
   CI-гейтами.
8. Web UI читает immutable run projections, безопасно обслуживает артефакты и
   корректно показывает retry, rejected, blocked, canceled и partial delivery.
9. Evals разделяются на deterministic contracts, behavioral fixtures, workflow
   fault-injection и статистические LLM quality evals.

## Scope

В scope входят Go CLI/control plane, built-in agents, config/schema, reports,
SQLite/web frontend, OpenSpec, CI и тестовая инфраструктура.

Вне scope: распределённый orchestrator, внешняя очередь, multi-host execution и
обязательный cloud backend. Система остаётся single-binary и local-first.

## Acceptance Criteria

- Ни один verdict-bearing stage не завершается успешно без одного валидного
  verdict.
- Старый output/status/summary не может удовлетворить контракт новой попытки.
- Exit code, run status, stage status, HTML и web UI не противоречат друг другу.
- Required human gate в non-interactive режиме не auto-approves.
- Delivery невозможен без validated plan и явного разрешения; `git add .` не
  используется.
- Все deterministic checks сохраняют command, exit code, stdout/stderr и tool
  version как evidence.
- Два запуска одной feature не перезаписывают evidence друг друга.
- `openspec validate --all --strict --no-interactive` проходит и выполняется в CI.
- Историческая web-страница показывает артефакты конкретного run, а не latest.
- Unit, integration, E2E и race suites покрывают false-green, stale artifact,
  retry, cancel, timeout, dirty workspace и partial delivery scenarios.

