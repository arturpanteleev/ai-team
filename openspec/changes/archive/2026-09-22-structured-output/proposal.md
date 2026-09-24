# Structureed output (OPS-6)

## Why

CI/агент/внешний оркестратор должны уметь **машиночитаемо** потреблять результат
команд `ai-team` (run/verify/export/gate) — стабильный JSON с полями, а не только
человеко-ориентированные цвета/formatted строки. Сейчас результат — свободный
текст на stdout/err; разобрать его надёжно нельзя, что ломает автоматизацию
(«запусти gate и оцени вердикт в коде»). Также отсутствует тихий режим для
вложенных/хитрых скриптов.

OPS-6 закрывает разрыв: `--json` (стабильный machine-readable JSON records на
stdout) и `--quiet/-q` (подавить второстепенный человеческий вывод), плюс
выделенный пакет вывода как единственная точка маршрутизации ключевых
результатов.

## What Changes

- **`pkg/logging`**: выделенный пакет вывода с глобальным режимом
  (`Default`/`Quiet`/`JSON`), типом `Record` (level, cmd, type, message, data,
  exit_code) и `Emit(Record)` — в JSON-режиме стабильный JSON на stdout, в
  human — краткая строка.
- **CLI-флаги**: `--json` и `--quiet/-q` глобально (до или после подкоманды);
  вырезаются из `os.Args`, чтобы подкоманды не падали на неизвестном флаге.
- **Маршрутизация результатов**: `cmdRun`, `cmdVerify`, `cmdExport`, `cmdGate`
  в JSON/quiet-режиме дополнительно эмитят структурированный `Record` результата
  с ключевыми полями (run_id, outcome, bundle_sha256, policy, status, exit).
- **Exit-коды не меняются**; человеческий вывод сохраняется (цвет) для
  интерактивного использования.

## Acceptance Criteria

1. `ai-team gate --json ...` выводит на stdout ровно один JSON object
   (`{"level":"ok","cmd":"gate","type":"gate",...}` с `status`, `policy`,
   `bundle_sha256`, `exit_code`), парсимый `encoding/json`.
2. `ai-team verify --json <bundle>` выводит JSON record `{"level":"ok",
   "cmd":"verify","type":"gate_bundle"|"run_bundle"|"run",...}`.
3. `--json` и `--quiet/-q` работают и до, и после имени подкоманды; подкоманда
   не падает на неизвестном флаге.
4. human-вывод и exit-коды идентичны до/после (регрессии нет): `make specs`
   65/65, `go test ./...` зелёные (кроме двух известных e2e baseline).
5. `pkg/logging` покрыт unit-тестом (JSON-структура, roundtrip режима).
