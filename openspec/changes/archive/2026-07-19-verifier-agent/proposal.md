## Why

В dev-task-workflow перед деплоем требуется **unified verification pass** — единый этап проверки, который:

- Сопоставляет фактический результат со всеми Acceptance Criteria из proposal.md
- Выполняет self-review (проверка diff, отсутствия лишних изменений, багов)
- Проверяет Definition of Done checklist

Текущие агенты `reviewer` и `tester` выполняют частичную проверку:
- **reviewer** — проверяет код против спецификации (качество, edge cases, security)
- **tester** — запускает тесты и проверяет AC

Но нет единого этапа, который:
- Проверяет **все** AC из proposal (не только из spec)
- Выполняет **self-review** diff
- Проверяет **DoD checklist** (принцип минимального изменения, отсутствие непреднамеренных изменений)
- Генерирует **сводный отчёт** о готовности к деплою

## What Changes

- Новый агент `verifier` с unified verification pass
- Порядок pipeline: `analyst → architect → coder → reviewer → tester → verifier → deployer`
- Verifier проверяет: AC из proposal, self-review diff, DoD checklist
- Вердикт: APPROVED / CHANGES_REQUESTED
- Артефакт: `verification.md` с результатами проверки

## Capabilities

### New Capabilities

- `verifier-verification-pass`: Unified verification pass — проверка AC, self-review, DoD checklist
- `verifier-integration`: Интеграция verifier в pipeline и registry

### Modified Capabilities

- `agent-orchestration`: Изменение порядка pipeline — добавление verifier между tester и deployer

## Impact

- `agents/verifier/def.yaml` — новый файл определения агента
- `agents/verifier/prompt.md` — новый промпт агента
- `pkg/agent/registry.go` — добавление verifier в default pipeline
- `pkg/config/load.go` — обновление дефолтного pipeline
- `.ai-team/artifacts/{feature}/verification.md` — новый артефакт
