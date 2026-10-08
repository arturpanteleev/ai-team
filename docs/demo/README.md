# Демо: `gate → bundle → verify`

Файлы для демонстрации `ai-team gate` без модели и API-ключей на небольшом
Python-репозитории. Пошаговое руководство с разбором сценариев и подключением
к GitHub Actions — [Проверки в CI без модели](../guides/ci-gate.md).

Быстрый запуск из корня ai-team (нужны `bash`, `git` и Go 1.26.9+):

```bash
bash docs/demo/run-demo.sh
```

Ожидаемый итог: `pass=0 (0), policy-fail=1 (1), junit-fail=1 (1)`.

| Файл | Назначение |
|---|---|
| [`run-demo.sh`](run-demo.sh) | Собирает ai-team, создаёт временный репозиторий и прогоняет три сценария |
| [`gate.yaml`](gate.yaml) | Конфигурация gate: `test_modify: required` и проверка `junit-xml` |
| [`pytest-pass.xml`](pytest-pass.xml), [`pytest-fail.xml`](pytest-fail.xml) | Готовые JUnit-отчёты вместо настоящего запуска pytest |
| [`ci-gate-demo.yaml`](ci-gate-demo.yaml) | Workflow GitHub Actions с зафиксированной версией ai-team; `go-version` в нём должна соответствовать сборке ai-team |
