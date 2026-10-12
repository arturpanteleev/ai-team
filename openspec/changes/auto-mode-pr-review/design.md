# Design: auto-mode stage ownership and PR-bound checks

Auto mode remains a run-scoped template projection. `RunEngine` pins a copy
where stages with an agent use the agent executor and normal graph confirmations
are automatic. The active template is untouched. The pipeline records `mode`
in its first controller-owned event so resume preserves the same behavior.
Existing explicit human gates for questions, specification agreement and
deferred delivery remain authoritative; agentless stages continue through the
typed human-input path.

For an agent-executed stage with a hard or agent check, the controller may
record one `auto_check_retry` event bound to the failed attempt and report
digest. It materializes a read-only feedback artifact from that report and
retries the same graph visit. A second failure follows the ordinary
`check_decision` approval path. Replay validates the retry event and recreates
the same input if a crash occurs before the retry starts.

`pkg/prchecks` resolves each PR through `gh pr view` and validates its URL,
trusted GitHub host, state, base/head names and full commit IDs. The default
trusted host is `github.com`; an enterprise host must be configured explicitly.
`project_checks` fetches the base/head refs into unique temporary refs, verifies
that Git resolved the same SHAs, then runs `gate.Run` in a fresh temporary
repository that contains only the pinned base/head commits and is checked out
at the PR head. A linked worktree is not used: its `.git` pointer would lead
back to the controller's common gitdir and configuration. Gate configuration is loaded from the pinned base
commit, not the candidate tree, so a PR cannot remove its own checks. Configured
commands run under Linux bubblewrap with network disabled, a read-only host
root, a writable isolated checkout and private home/temp paths. The controller
checkout, its gitdir and common gitdir (including the parent repository of a
linked worktree) and any custom `TMPDIR`/`TMP`/`TEMP` directory are masked with
empty tmpfs mounts. Credential-bearing variables are removed from both the
wrapper and child environments.
If bubblewrap is unavailable or cannot initialize, the check blocks before an
untrusted command runs. The isolated repository, temporary refs and temporary directory
are removed on success and error. `pr_checks_green` reads `gh pr checks`
between identity reads and fails if the base or head moved. Both rules share the
first validated URL/base/head identity, which is saved in the durable check
report for the later reviewer stage.

Before a reviewer stage with a preceding successful PR result, the controller
reads the accepted URL/base/head identity from the hash-verified check report,
then reads `gh pr diff` only while that exact identity remains stable. It writes
the diff and identity into immutable run-scoped files and passes both to the
reviewer with the existing declared inputs for specifications and technical
decision. A missing or mismatched identity blocks review when project checks
were configured. If no PR result exists, existing non-PR review flows remain
available. This preserves the B-35 deferred-delivery boundary: a PR created
only during terminal deferred delivery is not made available to an earlier
review stage by this change.

The PR runner only uses fetch, temporary refs, isolated temporary repositories and
read-only `gh` commands; controller commits, pushes and PR creation remain in
the existing delivery controller path.
