## Why

Система честно документирует «controlled, not OS-sandboxed» (README, FEATURES.md,
ARCHITECTURE.md), но не фиксирует, какие именно ограничения реально действуют
на каждом из четырёх containment-осей (filesystem, network, process, environment).
Это мешает:

- принимать осознанные решения о untrusted gate (`--allow-untrusted` заблокирован
  до P1-4);
- давать operator'у проверяемое подтверждение (receipt) о том, какие ограничения
  фактически включены;
- отличать trusted-local (текущий режим) от strict-sandbox (будущий с bubblewrap /
  landlock / sandbox-exec).

## What Changes

- **Threat model**: формализация четырёх containment-осей (fs, net, proc, env)
  с описанием атак и текущих mitigations для каждой.
- **Containment profile**: новый раздел в config — выбор профиля
  (`trusted-local` / `strict`),池政策 и backend-определение.
- **Per-axis receipt**: `ENFORCED` (OS-level), `PARTIAL` (application-level
  mitigations), `UNAVAILABLE` (нет mitigations) — для каждой оси в evidence
  manifest и verification output.
- **Env tightening**: расширение deny-list credential файлов (`.ssh/`, `.aws/`,
  `.gnupg/`, `credentials`), deterministic env allow-list с фиксацией в receipt.
- **Process cleanup receipt**: verify что все дочерние процессы завершены
  после отмены, с фиксацией в receipt.
- **CLI display**: `ai-team usage` и `ai-team verify` показывают containment
  receipt.
- **Gate integration**: `--allow-untrusted` требует receipt с `ENFORCED` или
  `PARTIAL` на os/net для strict-профиля (иначе BLOCKED).

## Capabilities

### New Capabilities

- `containment-profile`: threat model, profile config (trusted-local / strict),
  per-axis receipt semantics (ENFORCED / PARTIAL / UNAVAILABLE), env/process/fs
  mitigations, evidence manifest integration, CLI receipt display.

### Modified Capabilities

- `control-plane-safety`: добавить requirement на containment receipt в evidence
  manifest (run-level и gate-level).
- `cli-interface`: добавить `--containment` / receipt-вывод в usage/verify.

## Impact

- **pkg/config**: новая секция `Containment` в `Config` + `AgentConfig`
- **pkg/evidence**: `RunManifest` получает `ContainmentReceipt` поле
- **pkg/runtime**: env allow-list расширяется, process cleanup verification
- **pkg/gate**: `--allow-untrusted` читает receipt перед допуском
- **cmd/ai-team**: usage/verify/show получают receipt display
- **docs/ARCHITECTURE.md**: новая секция «Containment threat model»
- **FEATURES.md / README**: обновить «controlled, not OS-sandboxed» формулировку
