# Redaction + retention contract (P1-6)

## Proposal

**ID**: P1-6  |  **Trace**: privacy-контракт, блокер перед внешней публикацией
**Priority**: P1

### ЧТО

Полевая классификация, secrets-сканер, include/exclude policy и настраиваемая
retention; обязательный fail-closed блокер перед публикацией bundle наружу и
перед внешним архивом/SIEM.

### ПОЧЕМУ

Evidence содержит агентский вывод и снапшоты — в них могут протекать секреты.
Публикация bundle или внешний архив без сканирования = раскрытие кредов. Raw
logs должны быть opt-in, а retention — настраиваемой и при этом честной (без
обещаний legal compliance).

### Скоуп

1. `pkg/redact`: классификатор полей (secret/internal/public), консервативный
   secrets-сканер, include/exclude policy (repository-relative glob'ы через
   `pkg/scope`), verify (fail-closed) и redaction-копия `[REDACTED:...]`.
2. Config: `redaction` (include/exclude, raw_logs opt-in, disable_export_block)
   и `retention` (older_than, keep_last, prune_runs) + валидация.
3. CLI `ai-team redact verify|scan|redact`; `export` — fail-closed блокер;
   `gc` берёт дефолты из config.
4. OpenSpec delta.

### Критерии приёмки

- Сканер ловит известные формы секретов, не шумит на плейсхолдеры/env-ссылки.
- `redact verify` и `export` блокируются при секретах (по умолчанию).
- Классификация полей документирована.
- Retention настраивается из config; gc-флаги имеют приоритет.
- Raw logs — opt-in, никогда по умолчанию.

### Похожее / альтернативы

- Без сканера: ручная «тщательность» — уже сбоила, блокер обязателен.
- Постоянное хранение redacted-копий вместо scan-and-block: ломает
  самоописываемость evidence; мы делаем detached copy только по явному
  `redact redact`.
### Решения по спорным пунктам review (owner-response, R2)

Разбор трёх пунктов, по которым ревьюер расходился с дизайном; каждый прошёл
повторную проверку двумя независимыми ревизорами + владельцем.

1. **basic-auth rule over-match (`host:port@…`)** — отклонено как не-баг.
   Правило срабатывает на `scheme://user:pass@…` (встроенные креды в URL),
   fail-closed по дизайну: для security-сканера ложное срабатывание лучше
   пропуска. Ложные срабатывания снимаются через `redact.exclude`.

2. **`Finding.RedactedValue()` публичный, но не вызывается** — оставлен.
   Public API-контракт пакета `pkg/redact`; делегаты redaction вправе
   использовать его для кастомных правил. Мёртвый в текущих путях — не повод
   ломать публичный контракт.

3. **`redaction.raw_logs` / `scan_sensitive_fields` нигде не читаются** —
   осознанная де-скоупed-в-документацию. Зафиксировано как reserved future
   knobs в `pkg/config` (accept без ошибки, эффект появится с фичей).
