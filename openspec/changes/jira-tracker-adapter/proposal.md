# Jira task tracker adapter (B-80)

## Proposal

Add a small injectable task tracker adapter boundary and an optional one-way
Jira Cloud adapter. Successful stage events drive configured stage-to-status
transitions; every completed stage event produces a comment that links to the
corresponding ai-team task. External issue keys come from task `external_links`;
Jira credentials remain in the server process environment. Failures stay outside the pipeline result and are
recorded as durable task warnings.

The Jira settings live in the project's one selected template and do not add a
second template, graph, or runtime process selector.
