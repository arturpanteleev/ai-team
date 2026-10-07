# Restore a controller database snapshot

## Why

The online snapshot primitive needs a safe, testable recovery counterpart. The
first restore step should never overwrite the live controller database and
should verify the SQLite file before publication.

## Scope

Add `ai-team db restore --from <snapshot> --out <new-path>`. It checks
`PRAGMA integrity_check`, writes a consistent copy to a fresh destination with
mode 0600, and refuses symlink inputs or an existing output.

The command restores only SQLite contents. It does not restore run evidence,
artifact objects, secrets, configuration, or prove a full pilot recovery.
