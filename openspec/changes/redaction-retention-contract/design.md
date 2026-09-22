# Redaction + retention contract (P1-6) — design

## Проблема

Evidence хранит агентский вывод и снапшоты конфигов/workflow. Secrets-скан
+ redaction обязательны перед внешней публикацией (bundle, SIEM, архив).
Retention (уборка .ai-team) уже есть в `pkg/retention`/`ai-team gc`, но не
настраивается из config и не оформлена как контракт без legal-обещаний.

## Requirements

- Field classification: `secret`/`internal`/`public` по имени поля.
- Консервативный сканер: известные префиксы ключей + assignment-паттерны;
  без ложных срабатываний на плейсхолдеры (`<...>`, `example`, env-ссылки `$X`).
- include/exclude glob'ы в repository-относительном формате (`pkg/scope`).
- `redact verify` — fail-closed; `export` блокируется при находках (по умолчанию;
  opt-out `disable_export_block`).
- Retention настраивается в config; `ai-team gc` берёт дефолты, явные флаги
  переопределяют.
- Raw logs — opt-in; никогда не сохраняются без `redaction.raw_logs: true`.

## Design

### pkg/redact

- `scanner.go`: `Scan(data []byte) []Finding`; правила — private key block, AWS
  AKIA/ASIA, GitHub ghp_..., OpenAI sk-, Slack xox*, Google AIza, JWT eyJ, basic
  auth URL, secret assignment (ключей password/secret/token/api_key/access_key/
  private_key/client_secret/signing_key). Assignment-правило фильтруется
  `likelySecretValue`: len>=16 + uppercase + digit; reject плейсхолдеры
  ("example","placeholder","changeme","your-"), `<...>`, `${...}`, `$VAR`.
  Детерминированная сортировка результатов (line, reason, matched).
  `ClassifyField(name) FieldClass` — документированная таблица.
  `IsBinary`/`ScanFile` (safeio no-follow, лимит 8 MiB).
- `policy.go`: `Policy{Include,Exclude,FailOnSecrets,MaxBytes,SkipBinary}`,
  `Applies(rel)` через `scope.MatchAny`, `Validate` через `scope.Validate`.
- `verify.go`: `ScanDir` (WalkDir без симлинков/special), `Verify` →
  `Report{Verdict, Files, Bytes, Violations}`, ошибка при violations и
  FailOnSecrets. `RedactFile` — построчная замена `[REDACTED:reason]`.

### Config

`redaction` (include/exclude, raw_logs, disable_export_block) и `retention`
(older_than, keep_last, prune_runs) с Validate, allowlist/rawConfig/assignment.
`EffectiveFailExportOnSecrets` = включён по умолчанию, только явный
`disable_export_block: true` отключает. `EffectiveOlderThanDuration`/`KeepLast`
nil-safe дефолты 720h/20.

### CLI

- `ai-team redact verify|scan|redact` — новый command-файл; policy из config;
  scan-root = `.ai-team/runs` (или `--path`/`--run`); logging.Emit JSON.
- `export.go`: перед `export.Build` — `redact.Verify(runDir, policy)`; при
  ошибке — fail-closed, bundle не создаётся, `type: redact_block`.
- `gc.go`: `retention` дефолты из config при отсутствии явных флагов.

## Risks

- Сканер может пропускать нестандартные форматы секретов (например, ключи без
  префиксов) — это консервативный выбор: меньше ложных тревог, а блocker не
  должен блокировать экспорт из-за шума. Покрывается расширением правил.
- `disable_export_block` — явный отказ от защиты; имя выбрано с приоритетом
  fail-closed.

## Compliance note

Retention и redaction — технический контракт housekeeping, НЕ обещание legal
compliance: сроки хранения юридически определяются вне системы.