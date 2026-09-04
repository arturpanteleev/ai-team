# Security Policy

ai-team is a control plane that, in its default `trusted-local` profile,
executes agent tools and verification commands with the privileges of the
current OS user. This makes security a first-class concern: please report any
vulnerability promptly and privately.

## Reporting a vulnerability

**Do not open a public GitHub issue for security problems.**

Instead, disclose privately so we can triage and fix before publicising:

- **Email:** <security@ai-team.dev> (or the maintainer contact listed on the
  repository owner's GitHub profile)
- **GitHub Security Advisory:** use the private
  [Report a vulnerability](https://github.com/arturpanteleev/ai-team/security/advisories/new)
  form.

Please include:

1. Affected version(s) and OS/platform.
2. A minimal, reproducible description of the issue.
3. Which data or operations are at risk (e.g. secret exfiltration, privilege
   escalation, tampered evidence, remote code execution).
4. Anything else that helps us reproduce quickly.

You will receive an acknowledgement, and we will keep you updated as we
investigate and ship a fix. We ask that you allow a reasonable disclosure
window before making details public.

## Scope

This policy covers the `ai-team` repository itself (source, built binary,
web dashboard, checks/delivery/evidence code paths, and CI configuration).

### In scope

- Remote code execution, command injection, or privilege escalation.
- Secret/credential leakage to agents, checks, or evidence.
- Bypass of mutation scopes, verdict enforcement, approvals, or delivery
  preconditions (the "controller-owned effects" guarantees).
- Tampering with, or forging of, evidence/attestation bundles.
- Web dashboard auth/session/CSRF flaws.

### Out of scope (by design)

- **No OS-level sandbox:** in the `trusted-local` profile, agent tools and
  verification commands run with the current user's privileges. This is a
  documented, acknowledged limitation, not a vulnerability. Do not run ai-team
  on untrusted code or next to valuable secrets without external containment.
- Third-party dependencies (report those to their own projects) unless the
  ai-team integration is exploitable in a way the dependency alone is not.

## Supported versions

Only the latest released version and the current `master` are actively
patched. We recommend always using the newest release.

| Version | Supported |
| --- | --- |
| Latest release | :white_check_mark: |
| master | :white_check_mark: |
| Older releases | :x: |

## Security considerations for operators

- Run ai-team only in trusted, local repositories.
- For untrusted code or sensitive secrets, execute inside an external
  container/VM sandbox with strict filesystem/network/process limits and a
  disposable runtime (see `docs/ARCHITECTURE.md` and the deployment notes in
  `README.md`).
- Treat bundled evidence as integrity-protected but not necessarily
  authenticity-bearing unless signed with your own `--sign-key` and verified
  with `--verify-key`.

## Security hardening checklist

Before a release, the project enforces (see `make verify` and CI):

- Strict OpenSpec validation, `go vet`, module verification, `govulncheck`.
- Unit, E2E, race and coverage gates, including per-package safety floors.
- Frontend lint/tests/build and dependency audit.
- SHA-pinned CI actions with least-privilege `contents: read`.
