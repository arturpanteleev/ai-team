# CI/CD deploy adapter, first provider TeamCity (B-84)

## Proposal

Close the end of the idea-to-prod flow with a controller-owned deploy stage
(MAJ-08). A stage with `result: link`, `link_kind: build` and the built-in
`cicd-deploy` agent (`kind: deploy`, no model runtime) takes the pull request
published by the earlier controller PR stage, waits for its merge, proves
through Git that the merge commit is in the configured branch and carries the
PR change, triggers the configured CI/CD build for the branch tip, waits for its final status with a timeout and
cancellation, and records the build URL as the stage result.

The first provider is TeamCity (REST API, bearer token). The provider sits behind
a small `pkg/cicd.Provider` interface so other CI/CD systems can be added
without touching the pipeline. Credentials come only from the controller
process environment; failures become `integration_error` task warnings. Existing
templates are unchanged: the default `deploy` stage stays a human link stage.
