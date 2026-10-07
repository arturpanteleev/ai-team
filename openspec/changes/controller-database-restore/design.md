# Design

Reuse the SQLite `VACUUM INTO` snapshot writer after running SQLite
`integrity_check` against the input. The destination is a fresh path and uses
the same private staging, permission, sync, and no-replace publication rules as
database backup. A failed integrity check never publishes a destination.

The operator selects a new path and may switch the controller to it after
verifying that application-specific configuration and filesystem state have
also been restored. This CLI does not make that switch or claim full recovery.
