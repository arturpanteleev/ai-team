# Changelog

Все заметные изменения проекта описываются в этом файле.

Формат основан на [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
версионирование — [SemVer](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/arturpanteleev/ai-team/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/arturpanteleev/ai-team/releases/tag/v0.1.0
