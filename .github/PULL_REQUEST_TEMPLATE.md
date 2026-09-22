<!--
  Thanks for contributing to ai-team.
  Search existing PRs first to avoid duplicates.
-->

## Summary

<!-- One or two sentences: what this PR does and why. -->

## Related issue

<!--
  Link the issue this PR resolves so it closes on merge, e.g. `Closes #42`.
  The issue is a short pointer; this PR and its OpenSpec change are the source
  of truth. Where they disagree, the change is right (see CONTRIBUTING.md).
-->

Closes #

## OpenSpec change

<!--
  ai-team is developed spec-first through OpenSpec. If this PR changes
  observable behaviour, it must reference a change in openspec/ (proposal,
  design, specs, tasks). Pure fixes/tests/docs that do not change behaviour
  need no formal change (see CONTRIBUTING.md).
-->

- [ ] No behavioural change (test / refactor / docs / typo).
- [ ] Behavioural change — linked change in `openspec/changes/`.

## Testing

<!-- How was this verified? Include commands and expected results. -->

- [ ] `make build`
- [ ] `make test` (or targeted `go test ./pkg/...`)
- [ ] `make specs`
- [ ] `make verify` / CI checks pass
- [ ] e2e / frontend affected — `make test-e2e`, `npm run lint`, `npm test`

## Checklist

- [ ] Code formatted (`gofmt -l .` shows nothing).
- [ ] No secrets or credentials committed.
- [ ] Docs updated where behaviour/CLI changed (README, ARCHITECTURE).
