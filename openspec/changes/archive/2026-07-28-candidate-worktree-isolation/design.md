# Проектирование

## Разделение root

У run появляются два exact root:

- control target — пользовательский checkout с `.ai-team/state`, approvals,
  runs, web database и reports;
- candidate root — `.ai-team/worktrees/<run_id>`, созданный командой
  `git worktree add --detach <baseline>`.

`RunConfig.TargetDir` остаётся identity control target. `runtime.Task`,
checks, mutation guards, candidate digest и delivery получают candidate root.
Так lifecycle/resume не зависят от текущего process, но source live checkout
не становится рабочей областью агента.

## Candidate metadata и recovery

Atomic metadata в `.ai-team/state/candidates/<run_id>.json` содержит run,
control target, worktree path, base commit/tree и время создания. Resume
проверяет:

- safe run/path identity и отсутствие symlink;
- принадлежность worktree исходному Git repository;
- неизменность baseline и ancestry текущего HEAD;
- наличие candidate artifact root.

Новый run требует чистого live Git workspace до создания worktree.
Untracked/user-owned файлы не попадают в candidate, потому что worktree
строится только из baseline commit.

## Единая identity

После каждого attempt controller атомарно обновляет immutable-view
`candidate.json`: base commit/tree, source workspace SHA-256, tracked patch
SHA-256, changed paths/modes/digests, check evidence и attempts. Exact
workspace hash входит в subject каждого edge approval и в delivery
verification. Любая mutation меняет subject; старое решение нельзя применить
к новому request.

Artifacts являются control data, а не source. Агенты создают их внутри
candidate `.ai-team/artifacts`; после attempt controller обновляет live
projection и публикует immutable copies в run evidence.

## Delivery и promotion

Planner/executor запускаются в candidate root. Commit, branch, push и PR
относятся к проверенному candidate. Live checkout не переключает branch и не
получает source files автоматически. Отдельная promotion-команда и cloud
sandbox не входят в change.

## Не входит

- container/bubblewrap/sandbox-exec/Windows sandbox;
- network isolation;
- автоматическая promotion live checkout;
- несколько параллельных candidates одного run.
