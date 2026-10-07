# Design

`VACUUM INTO` produces a consistent SQLite snapshot while the source database
is active and includes committed WAL changes. The snapshot is written in a
fresh mode-0700 staging directory beside the destination, changed to mode
0600, synced, and hard-linked into place without replacement. The containing
directory is synced after publication. Failure removes the staging directory.

The CLI requires explicit source and destination paths. Output states that the
snapshot covers only SQLite controller state; filesystem evidence and artifact
storage need separate backup and restore procedures.
