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
is polled until the PR is `MERGED` (bounded by `merge_timeout`); `CLOSED` or a
different base branch is rejected. GitHub only names the merge commit; Git
decides: after `git fetch origin +refs/heads/<branch>:refs/remotes/origin/<branch>`
the merge commit must exist and be an ancestor of the fetched tip, and it must
contain the PR change: either the PR head is its ancestor (merge commit,
fast-forward) or `git diff <merge>^1 <merge>` equals `git diff <base> <prHead>`
with `base = git merge-base <prHead> <merge>^1`, compared after dropping hunk
line numbers and `index` lines (the normalization of `git patch-id`). The base
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
