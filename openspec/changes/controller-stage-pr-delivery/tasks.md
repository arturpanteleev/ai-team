- [x] Add controller-only `delivery.stage.execute` with pinned manifest, plan,
      runtime, and approval validation.
- [x] Persist an immutable controller stage receipt and recover PR projections
      after receipt or projection crash windows.
- [x] Keep terminal attestation separate and bind terminal records to the stage
      receipt without mutating attempt manifests.
- [x] Run existing hard PR checks after controller delivery and provide the PR
      result to later review and QA stages.
- [x] Add regressions for crash recovery, replay, terminal separation, and the
      default implementation/review/QA graph.
- [x] Resume `awaiting_plan` as a controller-only continuation without a new
      model run; e2e on mock runtime with a local bare remote and a `gh` stub.
- [x] Run targeted suites and `GOTOOLCHAIN=go1.26.9 make verify`.
