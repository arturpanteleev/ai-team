# containment-profile delta (P1-4 containment-profile)

## ADDED Requirements

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
Receipt публикуется отдельным evidence-файлом `{RunDir}/containment.json`
(параллельно `usage.json`; `run.json` immutable с момента `Start`):
```json
{
  "axes": {"fs": "PARTIAL", "net": "PARTIAL", "proc": "PARTIAL", "env": "PARTIAL"},
  "details": {
    "fs": {"symlink_reject": true, "worktree_isolation": true, "credential_deny": true},
    "net": {"tool_deny": true, "env_isolation": true},
    "proc": {"process_group_kill": true, "cleanup_verified": true},
    "env": {"allow_list": true, "config_dir_isolation": true, "credential_deny": true}
  },
  "profile": "trusted-local"
}
```

Receipt MUST генерироваться даже если run завершился с ошибкой.

#### Scenario: Receipt present in evidence

- **КОГДА** run завершается (terminal)
- **ТОГДА** `containment.json` MUST присутствовать в evidence-каталоге run
- **И** receipt MUST содержать ключи: fs, net, proc, env
- **И** каждый ключ MUST быть одним из: `ENFORCED`, `PARTIAL`, `UNAVAILABLE`

#### Scenario: Receipt absent для старого run

- **КОГДА** run завершился до P1-4 (нет `containment.json`)
- **ТОГДА** verify MUST treat receipt как `{all: UNAVAILABLE}` (backward compat)
- **И** MUST NOT fail verification

### Requirement: Containment levels

Каждая ось MUST иметь ровно один из трёх уровней:

- **ENFORCED**: OS-level confinement (bubblewrap, landlock, sandbox-exec).
  Применим только в strict profile с backend.
- **PARTIAL**: Application-level mitigations (safeio, tool deny, env allow-list,
  process-group kill). Текущий trusted-local.
- **UNAVAILABLE**: Нет mitigations для данной оси. strict профиль без backend
  MUST сходиться к этому уровню на всех осях.

#### Scenario: Уровень не принадлежит допустимому набору

- **КОГДА** receipt содержит неизвестный уровень для оси
- **ТОГДА** валидация receipt MUST вернуть ошибку (fail-closed)

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
4. Зафиксировать proc cleanup результат в receipt

#### Scenario: Process cleanup success

- **КОГДА** run отменён и все дочерние процессы завершены в течение 2 секунд
- **ТОГДА** процессный cleanup MUST быть Verified
- **И** `process_group_kill` MUST быть true

#### Scenario: Process cleanup timeout

- **КОГДА** run отменён и некоторые процессы не завершились за 2 секунды
- **ТОГДА** cleanup MUST честно сообщить `Timeout=true` (best-effort)
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

- **КОГДА** profile = strict но OS-sandbox backend недоступен (V1)
- **ТОГДА** receipt MUST содержать все оси = UNAVAILABLE
- **И** run с strict должен быть treated как неподдерживаемый (fail-closed),
  пока backend не включён

### Requirement: Gate allow-untrusted receipt check

`ai-team gate --allow-untrusted` MUST проверять containment receipt:
- Если receipt существует и все оси ≠ UNAVAILABLE → разрешить
- Если receipt отсутствует → BLOCKED (fail-closed)
- Если receipt содержит UNAVAILABLE ось → BLOCKED

#### Scenario: Gate untrusted с trusted-local receipt

- **КОГДА** gate выполняется с --allow-untrusted и trusted-local receipt
- **ТОГДА** gate MUST продолжить (PARTIAL receipts acceptable)

#### Scenario: Gate untrusted без receipt

- **КОГДА** gate выполняется с --allow-untrusted и нет receipt
- **ТОГДА** gate MUST быть BLOCKED (fail-closed)
