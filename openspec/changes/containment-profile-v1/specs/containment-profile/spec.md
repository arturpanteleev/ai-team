# containment-profile Specification

## Purpose
Containment threat model, per-axis receipt semantics, profile configuration,
env/process/fs mitigations и evidence integration.

## Requirements

### Requirement: Threat model axes

Система MUST определять четыре containment-оси: filesystem (fs), network (net),
process (proc), environment (env). Каждая ось MUST иметь:
- описание атак, которые она mitigates
- текущий containment level (ENFORCED / PARTIAL / UNAVAILABLE)
- конкретные mitigations для trusted-local профиля

#### Scenario: Operator запрашивает containment status

- **КОГДА** operator выполняет `ai-team usage <run_id>` или `ai-team verify`
- **ТОГДА** вывод MUST содержать per-axis receipt run

### Requirement: Containment receipt

Каждый run MUST генерировать containment receipt при завершении (terminal).
Receipt — JSON-объект в `RunManifest.ContainmentReceipt`:
```json
{
  "fs": "PARTIAL",
  "net": "PARTIAL",
  "proc": "PARTIAL",
  "env": "PARTIAL",
  "details": {
    "fs": {"symlink_reject": true, "worktree_isolation": true, "credential_deny": true},
    "net": {"tool_deny": true, "env_isolation": true},
    "proc": {"process_group_kill": true, "cleanup_verified": true},
    "env": {"allow_list": true, "config_dir_isolation": true}
  },
  "profile": "trusted-local"
}
```

Receipt MUST генерироваться даже если run завершился с ошибкой.

#### Scenario: Receipt absent для старого run

- **КОГДА** run завершился до P1-4 (нет receipt в manifest)
- **ТОГДА** verify MUST treat receipt как `{all: UNAVAILABLE}` (backward compat)
- **И** MUST NOT fail verification

### Requirement: Containment levels

Три уровня для каждой оси:

- **ENFORCED**: OS-level confinement (bubblewrap, landlock, sandbox-exec).
  Only одинаковый для strict profile с backend.
- **PARTIAL**: Application-level mitigations (safeio, tool deny, env allow-list,
  process-group kill). Текущий trusted-local.
- **UNAVAILABLE**: Нет mitigations для данной оси. Run НЕ должен продолжаться
  если profile = strict.

### Requirement: Env containment tightening

Env allow-list MUST additionally deny:
- Чтение файлов с путями содержащими `.ssh/`, `.aws/`, `.gnupg/`,
  `credentials`, `.env` (any depth)
- Это фиксируется в read-rules adapter'а и в receipt details

#### Scenario: Agent пытается прочитать credential файл

- **КОГДА** agent запрашивает чтение пути содержащего `.ssh/` или `.env`
- **ТОГДА** read-rule MUST deny access
- **И** в receipt details.env MUST быть `credential_deny: true`

### Requirement: Process cleanup verification

При отмене или остановке run controller MUST:
1. Отправить SIGKILL процессной группе (Unix) или taskkill /T /F (Windows)
2. Записать tracked PIDs в receipt
3. Проверить завершение всех PID (wait до 2 секунд)
4. Зафиксировать `proc.cleanup_verified: true/false` в receipt

#### Scenario: Process cleanup success

- **КОГДА** run отменён и все дочерние процессы завершены в течение 2 секунд
- **ТОГДА** receipt.proc MUST содержать `cleanup_verified: true`
- **И** `process_group_kill: true`

#### Scenario: Process cleanup timeout

- **КОГДА** run отменён и некоторые процессы не завершились за 2 секунды
- **ТОГДА** receipt.proc MUST содержать `cleanup_verified: false`
- **И** receipt MUST всё равно записан (best-effort)

### Requirement: Containment profile config

Config MUST поддерживать секцию:
```yaml
containment:
  profile: trusted-local  # trusted-local | strict
```

Profile defaults to `trusted-local` если не задан. Unknown profiles MUST
отклоняться при валидации config.

#### Scenario: Unknown profile

- **КОГДА** config содержит `containment.profile: unknown`
- **ТОГДА** `config.Validate()` MUST вернуть ошибку

#### Scenario: strict profile без backend

- **КОГДА** profile = strict но OS-sandbox backend недоступен
- **ТОГДА** run MUST быть отклонён с ошибкой `containment backend unavailable`
- **И** receipt MUST содержать все оси = UNAVAILABLE

### Requirement: Gate allow-untrusted receipt check

`ai-team gate --allow-untrusted` MUST проверять containment receipt:
- Если receipt существует и все оси ≠ UNAVAILABLE → разрешить
- Если receipt отсутствует и profile = trusted-local → разрешить (baseline)
- Если receipt absent и profile = strict → BLOCKED
- Если receipt содержит UNARRIER ось → BLOCKED

#### Scenario: Gate untrusted с trusted-local receipt

- **КОГДА** gate выполняется с --allow-untrusted и trusted-local receipt
- **ТОГДА** gate MUST продолжить (PARTIAL receipts acceptable)

#### Scenario: Gate untrusted без receipt

- **КОГДА** gate выполняется с --allow-untrusted и нет receipt
- **ТОГДА** gate MUST продолжить (backward compat для legacy run)
