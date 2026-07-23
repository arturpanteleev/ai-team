## 1. Implementation

- [x] 1.1 Replace `withoutEnvironmentKeys` deny-list with `allowedEnvironmentKeys`/`withAllowedEnvironmentKeys` allow-list
- [x] 1.2 Define baseline allow-list (PATH, HOME, locale/session variables)
- [x] 1.3 Support `AI_TEAM_OPENCODE_ENV_ALLOW` for explicit per-project/per-user opt-in

## 2. Verification

- [x] 2.1 Test: variable not on the allow-list does not reach the subprocess environment
- [x] 2.2 Test: variable named in `AI_TEAM_OPENCODE_ENV_ALLOW` does reach it
- [x] 2.3 Test: PATH always reaches it regardless of configuration
- [x] 2.4 Confirm existing OpenCodeIsolationEnvironment tests (permission JSON, edit/read rules, execution-surface rejection) are unaffected
- [x] 2.5 Confirm e2etest (real pipeline run through the mock opencode CLI) still passes end to end
