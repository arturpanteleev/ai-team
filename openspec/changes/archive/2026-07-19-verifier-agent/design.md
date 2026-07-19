## Context

Текущий pipeline содержит 6 агентов: analyst → architect → coder → reviewer → tester → deployer.

- **reviewer** проверяет код против спецификации (качество, edge cases, security). Вердикт: APPROVED/REJECTED/CHANGES_REQUESTED.
- **tester** запускает тесты и проверяет AC из spec. Вердикт: PASS/FAIL.

Нет единого агента, который:
- Проверяет **все** AC из proposal.md (не только из spec)
- Выполняет **self-review** diff (проверка лишних изменений, багов)
- Проверяет **DoD checklist** (принцип минимального изменения, security, performance)
- Генерирует **сводный отчёт** о готовности к деплою

## Goals / Non-Goals

**Goals:**
- Создать нового агента `verifier` с unified verification pass
- Verifier проверяет: AC из proposal, self-review diff, DoD checklist
- Вердикт: APPROVED / CHANGES_REQUESTED
- Артефакт: `verification.md`
- Интегрировать в pipeline: `... → reviewer → tester → verifier → deployer`

**Non-Goals:**
- Замена reviewer или tester (они остаются)
- Автоматическое исправление ошибок (verifier только проверяет)
- Изменение pipeline logic (verifier — обычный агент)

## Decisions

### Decision 1: Verifier как отдельный агент

**Выбор:** Новый агент `verifier` с собственным `def.yaml` и `prompt.md`.

**Альтернативы:**
- Расширить reviewer — нарушает single responsibility
- Расширить tester — tester фокусируется на тестах, verifier на holistic review
- Сделать частью pipeline logic — требует изменения Go кода, менее гибко

**Rationale:** Отдельный агент позволяет гибко настраивать модель/effort, переиспользовать существующую инфраструктуру агентов, и не загромождать pipeline logic.

### Decision 2: Вердикт APPROVED/CHANGES_REQUESTED

**Выбор:** Два вердикта как у reviewer.

**Альтернативы:**
- PASS/FAIL как у tester — не соответствует spirit verification
- Множественные вердикты (APPROVED/PARTIAL/REJECTED) — избыточно

**Rationale:** Бинарный вердикт прост и однозначен. CHANGES_REQUESTED позволяет loopback к coder через существующий механизм.

### Decision 3: Входные данные

**Выбор:** Verifier принимает: proposal, specs, review, test-report.

**Альтернативы:**
- Только proposal + specs — недостаточно для self-review
- Все артефакты — избыточно, verifier не нуждается в design.md

**Rationale:** Proposal для AC, specs для контекста, review и test-report для проверки результатов предыдущих этапов.

## Risks / Trade-offs

- **[Risk]** Дополнительный этап замедлит pipeline → **Mitigation:** Verifier быстр чем coder/reviewer/tester; gate-точка перед deployer уже есть
- **[Risk]** Duplication проверок с reviewer/tester → **Mitigation:** Verifier проверяет holistic (AC + DoD), reviewer — код, tester — тесты. Разные фокусы
