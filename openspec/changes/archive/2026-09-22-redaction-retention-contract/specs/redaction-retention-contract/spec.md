# redaction-retention-contract delta (P1-6)

## ADDED Requirements

### Requirement: Secrets scanner over evidence

Evidence content MUST be scannable for known secret shapes before it can be
published or archived externally. The scanner is conservative (known prefixes
and assignment patterns); confirmed findings are recordable and machine-readable.

#### Scenario: Scanner detects common secrets

- **WHEN** a file contains a private key block, AWS/GitHub/OpenAI/Slack/Google
  token, JWT, basic-auth URL or a secret assignment (`PASSWORD=...` with a
  high-entropy value)
- **THEN** the scan reports findings with a reason, matched value and line
- **AND** placeholder/vault/env-reference values are NOT reported

#### Scenario: Redact command produces sanitized copy

- **WHEN** `ai-team redact redact` runs over evidence
- **THEN** a detached copy is written where every secret match is replaced
  with `[REDACTED:<reason>]`

### Requirement: Fail-closed export blocker

Exporting a run bundle externally MUST be blocked (fail-closed) when evidence
contains secrets, unless the operator explicitly disables the blocker.

#### Scenario: Export refused on secrets

- **WHEN** `ai-team export <run_id>` runs over evidence containing secrets
- **THEN** export MUST fail and name the redaction reason
- **AND** no bundle or verified-export record is produced

#### Scenario: Blocker is on by default

- **WHEN** no `redaction` section is configured
- **THEN** the export blocker MUST be enabled
- **AND** only `redaction.disable_export_block: true` disables it (never by absence)

### Requirement: Field classification contract

Sensitive field names MUST be classifiable as `secret`/`internal`/`public` so
future consumers can enforce field-level redaction without inventing their own rules.

#### Scenario: Secret field names classify as secret

- **WHEN** a field key matches `password`, `token`, `secret`, `api_key`,
  `access_key`, `private_key`, `client_secret`, `signing_key`
- **THEN** it classifies as `secret`

### Requirement: Configurable retention without compliance promise

Retention of `.ai-team` artifacts MUST be configurable (age, keep-last count,
optional pruning of exported evidence), and MUST be described as a technical
housekeeping contract — not a legal-compliance obligation.

#### Scenario: Retention section drives gc defaults

- **WHEN** `retention.older_than`, `retention.keep_last` or `retention.prune_runs`
  are set in config
- **THEN** `ai-team gc` uses them as defaults
- **AND** explicit `gc` flags take precedence over config

#### Scenario: Invalid retention config is rejected

- **WHEN** `older_than` does not parse as a duration or `keep_last` is negative
- **THEN** config validation MUST fail