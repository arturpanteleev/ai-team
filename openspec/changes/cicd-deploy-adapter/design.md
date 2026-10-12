# Design: CI/CD deploy adapter

`pkg/cicd.Provider` exposes `Trigger`, `Build` (status) and `Cancel`. The
generic `cicd.Wait` polls a build through an injected `Clock`, stops at the
configured timeout or on context cancellation and then asks the provider to
cancel the build. A finished build is accepted only with status success, the
exact expected revision and a web URL. `cicd.Deploy` adds the merge proof and a
receipt in `.ai-team/state/deploy/<run>-<stage>.json` bound to the run and
stage. The receipt is only a pointer: a retried stage re-reads the recorded
build from the provider (status, link, build configuration), then resumes an
unfinished build or reuses a successful one for the same revision; if the
branch tip moved, an unfinished old build is canceled and a new one is
triggered. Status polls retry `TransientError`s (network, HTTP 5xx, 429) until
the deadline; triggers are never retried.

The deploy target is resolved from the controller PR receipt (PR URL and the
head commit the controller pushed). `gh pr view --json state,baseRefName,mergeCommit`
is polled until the PR is `MERGED` (bounded by `merge_timeout`); `CLOSED`, a
different base branch, a PR outside the repository captured at run start
(`TaskRepository`) or a `headRefOid` other than the published commit is
rejected. The branch tip comes from `gh api repos/<o>/<r>/branches/<b>` for the
captured repository: a checkout-local `url.*.insteadOf` cannot redirect it.
All Git work happens in a private temporary bare repository (`GIT_DIR` in the
system temp directory, removed afterwards) that fetches
`refs/heads/<b>` and `refs/pull/<n>/head` from the captured repository URL; the
fetched objects must include that exact tip. Nothing in the agent-writable
checkout is read. Git runs with `GIT_CONFIG_NOSYSTEM`,
`GIT_CONFIG_GLOBAL=/dev/null`, `GIT_NO_REPLACE_OBJECTS`, an empty graft file,
inherited `GIT_*` location/config variables removed, and `GIT_CONFIG_COUNT`
entries `credential.helper=` (reset), `credential.helper=!gh auth git-credential`,
`core.commitGraph=false` and `core.hooksPath=/dev/null`. Private repositories
therefore authenticate with the controller's gh login only. GitHub only names the merge commit; Git decides: the merge commit must
exist and be an ancestor of the tip, and it must
contain the PR change: either the PR head is its ancestor (merge commit,
fast-forward) or `git diff <merge>^1 <merge>` equals `git diff <base> <prHead>`
with `base = git merge-base <prHead> <merge>^1`, built with `--no-textconv --no-ext-diff --binary --ignore-submodules=none` and
explicit context, rename, prefix and algorithm options, compared after dropping
hunk line numbers and `index` lines (the normalization of `git patch-id`). The base
is computed by Git rather than read from the delivery plan's `baseline_head`,
which keeps the proof independent of local plan files and matches GitHub's
squash semantics. A multi-commit rebase merge is rejected.

The deployed revision is the fetched branch tip, not the merge commit and not
the PR head. Deploying the PR head would roll back changes merged after the PR
was forked; deploying the merge commit would roll back changes merged after it.
The tip contains the merge commit by proof and is the newest reviewed state of
the branch. Git and gh run through `EnvFilteredRunner`, which removes the
TeamCity token variable from their environment; their diagnostics are not
copied into errors.

The TeamCity adapter validates `base_url` as https without credentials, query
or fragment, wraps the injected HTTP client with a redirect policy that follows
only https redirects on the same host, sends the token as
`Authorization: Bearer`, limits response size, drops request URLs from
transport errors and redacts the token defensively. A build's `webUrl` must be
https on the configured host. Every build read must belong to the configured `buildTypeId`. The trigger pins
`revisions.revision[].version`
to the SHA with `failOnMissingRevisions`, and the finished build's reported
revision is compared with the expected SHA.

In the pipeline, `kind: deploy` agents are controller operations like
`kind: delivery`: no runtime, no usage, no workspace mutation guard (the fetch
updates Git metadata). The stage body is `runControllerDeploy`. The provider is
injected with `pipeline.WithDeployProvider` only by the local controller
(`ai-team run`, local `ai-team web`); a sandboxed worker never builds it and a
deploy stage there fails closed. Deterministic negatives (not merged, failed
build, wrong revision, misconfiguration) mark the attempt as validation failure;
transport errors and timeouts are infra failures. Each failure is projected to
the task event feed as `integration_error` with `integration: cicd`.

Before a trigger the receipt records `trigger_pending`. Only a definite
rejection (`HTTPStatusError` 4xx except 408/429) clears it; a lost, unreadable
or 5xx response keeps it, and the next attempt refuses
to trigger until an operator checks TeamCity and removes the receipt. Trigger
failures are never treated as retryable.

A deploy stage does not use `stage_timeout`; its budget is
`merge_timeout + timeout + 5m`, so a slow merge cannot cancel a running
deployment. The run budget still applies.
