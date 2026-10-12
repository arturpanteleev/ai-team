# CI/CD deploy adapter (B-84) — tasks

- [x] Add `pkg/cicd` provider interface, injectable-clock wait with timeout,
  cancellation and transient-poll retry, merge wait with Git merge proof
  (merge commit and squash), branch-tip deploy target and a receipt that is
  always re-read from the provider.
- [x] Add the TeamCity REST adapter with https-only base URL, same-host
  redirects, bearer token from the environment and secret-free errors.
- [x] Add strict `cicd.teamcity` template configuration and the built-in
  controller-owned `cicd-deploy` agent (`kind: deploy`).
- [x] Execute deploy stages in the controller, write the build link as the stage
  output and record `integration_error` on failure.
- [x] Cover success, failure, timeout, cancellation, wrong SHA, unmerged and
  closed PR, squash on a moved branch (real Git), tampered receipt, foreign
  build configuration, 5xx retry, foreign redirect and token leakage.
- [x] Update configuration, security, pipeline and architecture docs.
- [x] Run `GOTOOLCHAIN=go1.26.9 make verify` on the final tree.
