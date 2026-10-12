# Design: CI/CD deploy adapter

`pkg/cicd.Provider` exposes `Trigger`, `Build` (status) and `Cancel`. The
generic `cicd.Wait` polls a build through an injected `Clock`, stops at the
configured timeout or on context cancellation and then asks the provider to
cancel the build. A finished build is accepted only with status success, the
exact expected revision and a web URL. `cicd.Deploy` adds the merge proof and a
receipt in `.ai-team/state/deploy/<run>-<stage>.json`: a retried stage resumes
polling a recorded unfinished build or reuses a recorded successful one instead
of triggering a duplicate deployment.

The merge proof does not trust any provider or GitHub response:
`git fetch origin +refs/heads/<branch>:refs/remotes/origin/<branch>`, then
`git rev-parse --verify <sha>^{commit}` must return the same SHA and
`git merge-base --is-ancestor <sha> refs/remotes/origin/<branch>` must succeed.
Git diagnostics are not copied into errors because they can contain remote URLs
with credentials. Squash and rebase merges change the SHA and are therefore not
accepted; this is intentional for the first version.

The TeamCity adapter validates `base_url` as https without credentials, query
or fragment, wraps the injected HTTP client with a redirect policy that follows
only https redirects on the same host, sends the token as
`Authorization: Bearer`, limits response size, drops request URLs from
transport errors and redacts the token defensively. A build's `webUrl` must be
https on the configured host. The trigger pins `revisions.revision[].version`
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
