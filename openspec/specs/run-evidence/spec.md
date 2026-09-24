# run-evidence Specification

## Purpose
Immutable run/attempt manifests и hash-chained event log как источник истины; SQLite/web — восстанавливаемые projections.
## Requirements
### Requirement: Run and attempt identity
Run evidence MUST сохранять identity attempts, transitions и всех человеческих
approval requests/decisions в одной verified chain.

#### Scenario: Approval evidence
- **КОГДА** approval создаётся или получает решение
- **ТОГДА** event MUST содержать approval_id, subject hash, actor/action при
  решении и связанные from/to stages

### Requirement: Artifact provenance
Every published artifact MUST record producer, run, attempt, size and SHA-256 hash.

#### Scenario: Stale output
- **WHEN** a stage exits without publishing a fresh output for its current attempt
- **THEN** an output from an earlier attempt MUST NOT satisfy the contract

### Requirement: Retry invalidation
Retry and loopback MUST invalidate downstream evidence from superseded attempts.

#### Scenario: Loopback to coder
- **WHEN** verifier sends the workflow back to coder
- **THEN** previous reviewer, tester and verifier outputs MUST NOT be reused as current evidence

### Requirement: Atomic publication
Artifacts and manifests MUST be published atomically.

#### Scenario: Interrupted write
- **WHEN** a process is terminated during output creation
- **THEN** the partial file MUST NOT be accepted as a completed artifact

### Requirement: Non-Unix workspace lock staleness recovery
On platforms without a native advisory-lock primitive, the workspace lock MUST record the acquiring process's pid and MUST reclaim an existing lock only when there is positive evidence the recorded pid no longer exists; inconclusive evidence MUST leave the lock in place.

#### Scenario: Lock holder no longer exists
- **WHEN** an existing lock's recorded pid can be positively confirmed as no longer running
- **THEN** the lock MUST be reclaimed and re-acquired by the new caller

#### Scenario: Inconclusive evidence
- **WHEN** the existing lock's pid file is missing, unreadable, unparseable, or its liveness cannot be positively disproven
- **THEN** the lock MUST NOT be reclaimed and acquisition MUST fail exactly as before this capability existed

### Requirement: Evidence compiled graph

Immutable workflow snapshot MUST содержать фактически исполненный compiled
graph, включая entry, edges, approval policies и max_visits.

#### Scenario: Resume graph run

- **КОГДА** graph run возобновляется после restart
- **ТОГДА** engine MUST проверить snapshot digest
- **И** восстановить visit counters и next node из immutable evidence/lifecycle

### Requirement: Candidate metadata в run evidence

Run evidence MUST сохранять candidate metadata и актуальную `candidate.json`
без зависимости от существования live projection.

#### Scenario: Evidence проверяется после run

- **КОГДА** worktree позднее удалён
- **ТОГДА** base identity, patch hash, changed files, checks и attempts MUST
  оставаться доступны в immutable run evidence

### Requirement: Evidence verification completeness

`ai-team verify <run_id>` MUST проверять каждый файл, который объявлен
проверяемым, и MUST NOT утверждать проверку того, что не проверяет. Terminal
anchor MUST фиксировать digest run manifest. Каждая запись `inputs[]`/
`outputs[]` attempt manifest MUST сверяться с фактическим артефактом по типу,
размеру и SHA-256, а файл внутри каталога попытки, не покрытый ни одной
записью, MUST отвергаться. Файлы вне проверяемого набора (raw logs, HTML-
отчёты, производные метрики) MUST NOT описываться как tamper-evident.

#### Scenario: Подменён архивный артефакт попытки

- **КОГДА** файл, на который указывает `outputs[].evidence_path`, изменён,
  удалён или дополнен посторонним файлом
- **ТОГДА** verify MUST завершиться ошибкой с указанием попытки и артефакта

#### Scenario: Подменён run manifest

- **КОГДА** изменено содержимое `run.json` (feature, target dir, controller
  identity или provenance digests)
- **ТОГДА** verify MUST завершиться ошибкой несовпадения с digest в anchor

#### Scenario: Anchor без digest run manifest

- **КОГДА** anchor создан схемой, не фиксирующей digest run manifest
- **ТОГДА** verify MUST отказать fail-closed, а не сообщать об успешной проверке

### Requirement: Post-terminal evidence records

Записи после terminal anchor MUST быть проверяемы теми связями, которые у них
есть (delivery record, containment receipt): delivery
record MUST нести обязательный self-integrity digest и MUST сверяться с
`delivery_deferred` событием цепочки, attestation и provenance; containment
receipt MUST детерминированно выводиться из профиля исполнения и MUST
пересчитываться при verify. Receipt MUST NOT содержать флагов, утверждающих
наблюдение, которого система не делает.

#### Scenario: Подменён результат доставки

- **КОГДА** в `delivery.json` изменены поля или удалён `record_sha256`
- **ТОГДА** чтение record MUST завершиться ошибкой, а verify — отказом

#### Scenario: Подменён containment receipt

- **КОГДА** уровень оси или флаг в `containment.json` изменён
- **ТОГДА** verify MUST завершиться ошибкой несовпадения с каноническим receipt

#### Scenario: Внешние факты доставки

- **КОГДА** evidence содержит `commit_sha`/`pr_url`
- **ТОГДА** система MUST NOT утверждать их локальную проверяемость: они
  подтверждаются только против самого репозитория
