# Controller database snapshot

## Why

The cloud pilot still lacks an operational recovery path. The first safe unit is
a consistent copy of the controller SQLite database, including committed WAL
state, without implying that this single file is a complete system backup.

## Scope

Add `ai-team db backup --db <path> --out <path>`. It must not replace an
existing destination, must restrict snapshot permissions, and must publish only
after SQLite finishes a consistent online snapshot. Run evidence, artifacts,
secrets, and other files are explicitly outside this command's scope.

This is one backup primitive. It does not provide a full recovery workflow or
close MAJ-07.
