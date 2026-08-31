## Context

ai-team запускает LLM-агентов (opencode, codex, claude) в candidate worktree
с контейнерными permissions (edit/read/deny), env allow-list (~15 vars) и
process-group kill при отмене. Нет OS-level sandbox: агент может читать
`~/.ssh/`, `.env` на хосте, делать outbound connections, видеть host processes.
Система честно документирует «controlled, not OS-sandboxed» и блокирует
`gate --allow-untrusted` до P1-4.

## Goals / Non-Goals

**Goals:**
1. Формализовать threat model по 4 осям (fs, net, proc, env) с attack scenarios.
2. Ввести containment profiles: `trusted-local` (текущий baseline) и `strict`
   (будущий OS-sandbox).
3. Per-axis receipt в evidence manifest: `ENFORCED` / `PARTIAL` / `UNAVAILABLE`.
4. Tighten env и process mitigations в trusted-local профиле.
5. CLI display receipt в usage/verify.
6. Gate --allow-untrusted проверяет receipt перед допуском.

**Non-Goals:**
- Реальный OS-sandbox backend (bubblewrap, landlock, sandbox-exec) — V2.
- Windows containment beyond current taskkill.
- Multi-tenant isolation.
- CPU/memory resource limits.

## Decisions

### D1: Receipt per axis, not per profile

Receipt фиксирует реальное состояние каждой оси, а не просто имя профиля.
Два trusted-local run'а могут иметь разные receipts если один использует
bubblewrap (future), а другой нет. Receipt — source of truth для gate decisions.

### D2: trusted-local профиль — application-level controls

В trusted-local профиле containment = текущие mitigations + усиления V1:
- **fs**: safeio (symlink reject) + candidate worktree isolation + deny-list
  `.env*`, `.ssh/`, `.aws/`, `.gnupg/`, `credentials` в read-rules
- **net**: tools deny (bash, webfetch, websearch) + env isolation
- **proc**: process-group kill + PID tracking + verified cleanup
- **env**: strict allow-list + credential file deny

Receipt = `PARTIAL` на всех осях (application-level, не OS-enforced).

### D3: strict profile — fail-closed without backend

Strict profile требует OS-sandbox backend. Если backend недоступен —
containment receipt = `UNAVAILABLE`, run MUST быть отклонён. Не полагаемся
на application-level mitigations для strict.

### D4: Receipt в evidence

`run.json` immutable с момента `Start`, поэтому receipt пишется отдельным
evidence-файлом `{RunDir}/containment.json` (параллельно `usage.json`) на
terminal-завершении. JSON-объект `{axes: {fs: PARTIAL, net: PARTIAL,
proc: PARTIAL, env: PARTIAL}, details: {...}, profile: "trusted-local"}`.
Legacy-раны без файла валидны — verify/gate трактуют оси как `UNAVAILABLE`
(без fail). При генерации attest statement receipt входит в predicate как
доказательство containment-состояния run. CLI `ai-team usage` печатает
per-axis статус из `containment.json`, если он есть.

### D5: Gate --allow-untrusted требует receipt

`--allow-untrusted` разрешён только если передан containment receipt с ни одной
осью ≠ `UNAVAILABLE`. Отсутствие receipt или receipt с `UNAVAILABLE` осью →
BLOCKED (fail-closed). Для strict profile без backend receipt всегда
`UNAVAILABLE`, потому untrusted с strict → BLOCKED. Legacy baseline без receipt
и без `--allow-untrusted` продолжает работать как раньше (флаг не используется).

### D6: Process cleanup verification

При отмене/остановке run controller:
1. Отправляет SIGKILL процессной группе (текущий код).
2. Ждёт до 2 секунд завершения всех tracked PID.
3. Записывает в receipt `proc: {cleanup_verified: true/false}`.

Если cleanup_verified = false → receipt = PARTIAL (с пометкой
`cleanup_unverified`).

## Risks / Trade-offs

- **Receipt может быть подделан**: receipt хранится в controller-writable
  evidence, не в signed attestation. Для V1 это acceptable — receipt нужен
  для diagnostics и gate decisions, не для крипто-proof.
- **strict без backend = useless**: осознанный trade-off: лучше fail-closed
  чем ложное чувство безопасности.
- **macOS sandbox-exec deprecated**: в V2 может потребоваться альтернатива
  (Nomad/pkg/sandbox).
