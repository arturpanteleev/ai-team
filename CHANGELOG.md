# Changelog

Все заметные изменения проекта описываются в этом файле.

Формат основан на [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
версионирование — [SemVer](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.2.0] - 2026-09-04

Второй публичный релиз. Фокус — runtime-адаптеры, происхождение и безопасность,
детерминированный gate, приватность и подготовка к публичному open-source.

### Added

- **Runtime-адаптеры** поверх тонкого контракта `RuntimeAdapter` с registry
  (P1-1): OpenCode (P1-1), Codex (`codex exec`, launch-carrying `Command`
  contract, ADP-1) и Claude Code (`claude -p`, JSON usage, deny-list policy,
  ADP-2).
- **Происхождение и доверие:** authority-bearing provenance manifest v1 с
  обнаружением drift при resume (V0-2), in-toto-совместимый attestation
  predicate v1 (V0-3), test-mutation provenance и policy (V0-1) и
  DSSE-signed bundle (ed25519, stdlib-only, PAE по спецификации in-toto; P1-5).
- **Контейнерная политика:** containment profile v1 c per-axis receipt
  (fs/net/proc/env; P1-4) и детерминированный diff-policy gate MVP (V0-5) с
  typed checks, attestation bundle и риск-сигналами (V0-8).
- **Самодостаточные артефакты:** экспорт проверенного run/gate-bundle с
  fail-closed гарантиями (V0-4, V0-7, V0-0) и generic junit-xml typed adapter
  (V0-6).
- **Приватность (P1-6):** privacy-контракт — secrets-скан evidence, fail-closed
  export verify и detached-копия с заменой секретов на `[REDACTED:...]`.
- **Жёсткий бюджет и честность:** hard run budget + attested usage truthfulness
  (P1-7) и fail-closed evidence verification on resume (OPS-3).
- **CLI/UX:** структурированный JSON/quiet вывод + `pkg/logging` (OPS-6).
- **CI/infra:** импорт ограниченного объяснимого набора checks из GitHub Actions
  без исполнения произвольного YAML (P1-8), конфигурируемый tree-hash ignore
  list (OPS-2), Dependabot для SHA-pinned Actions (OPS-10).
- **Open-source:** публичный сайт документации (docsgen → GitHub Pages), release
  workflow (multi-platform бинарники), SECURITY, CODE_OF_CONDUCT, issue/PR
  шаблоны, Dependabot.

### Changed

- Консолидированный gate delivery, deferred gate approvals и повторное
  использование на re-traversal (APF-1): `verifier` и `deployer` переработаны.
- Evidence-цепочка и approve-модель: канонический delivery plan подтверждается
  исключительно по точному SHA-256 (роль `release_manager`).

### Removed

- Легаси-пути approvals и внутренние планировочные артефакты (plans/,
  BACKLOG, TASKLOG, AUDIT, kimi-review) — трекинг задач перенесён в GitHub
  Issues.

## [0.1.0] - 2026-08-23

Первый публичный релиз ai-team — строгого workflow-контроллера AI-агентов.

### Added

- Строгий workflow-контроллер AI-агентов: план → детерминированные проверки →
  enforcement вердиктов → delivery, контролируемая человеком доставка через
  SHA-256 approval канонического плана (`--approve-plan`).
- Graph schema v4: доменные state/outcome типы и чистые переходы в `pkg/workflow`.
- Approvals и RBAC: унифицированные approvals без legacy-путей, контроль
  мутаций репозитория через scope-политику (`pkg/scope`) и no-follow safeio.
- Candidate worktree и sandbox isolation: изоляция изменений кандидата,
  process-group supervision с гарантированным kill.
- Web dashboard: HTTP API + SQLite store, React-фронтенд, HTML-отчёты,
  append-only evidence manifests.
- Scheduler для оркестрации задач разработки (dev-task-workflow) в ручном
  и автоматическом режиме.
- CLI: `ai-team version`, exit-коды run (OK/FAILED/BLOCKED/USER_STOPPED),
  E2E-тесты на mock-opencode.

[Unreleased]: https://github.com/arturpanteleev/ai-team/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/arturpanteleev/ai-team/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/arturpanteleev/ai-team/releases/tag/v0.1.0