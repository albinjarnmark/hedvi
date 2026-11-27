# Backup and restore test (required before external customers)

Quarterly and before launch: restore the latest PITR point to a new instance, run `hedvi migrate`, then **run retention once before anyone uses the data** so content/metadata past retention is not reintroduced. Verify row counts, a sample email timeline and that payload references point to existing objects (orphans are expected to be swept). Record start time, restore duration and RPO observed in `docs/operations/restore-log.md`.
