# OTLP edge storage controls

The OTLP owner can act as a persistent edge agent between a local application
and a remote Collector. These controls are opt-in and do not alter the legacy
log journal or the default OTLP receipt-retention behavior.

## Configuration and scope

Add the following inside `settings.otlp`, together with the existing listener,
authentication, destinations and separate persistent journal directories:

```yaml
max_wal_bytes: 1073741824       # 1 GiB logical WAL admission threshold
max_storage_bytes: 2147483648   # 2 GiB whole-root admission threshold
storage_scan_max_entries: 4096 # metadata entries per scan; refusal on exhaustion
storage_scan_timeout: 25ms     # combined admission scan time between syscalls
receipt_gc: true               # upgrades generation metadata; read rollback below
```

`max_storage_bytes` defaults to zero (disabled), must be at least
`max_wal_bytes` when enabled, and is limited to 2^50 bytes. Admission accounts
regular files throughout the owned root, including WAL, receipts, recovery files
and retained incomplete evidence. Each WAL/root scan visits at most
`storage_scan_max_entries` entries (default 4096, range 1–1048576), including
directories. The two scans share `storage_scan_timeout` (default 25ms, range
1ms–1s). A scan that exhausts either budget refuses the request with HTTP 503
without appending, advancing identity, or poisoning the journal. It does not
treat a partial size sum as free space. Scanning stops early once it proves the
byte threshold is already exceeded. An oversized envelope is refused before
any metadata scan. The scans retain all evidence, and recovery/replay continues.
Operators should measure and tune these budgets on the edge host. A pending
gap or retained quarantine can exceed the entry budget even below the byte cap;
repair the destination or increase a measured budget without deleting evidence.
Context checks run between entries; a blocked kernel metadata syscall or fsync
cannot be interrupted, so the timeout is not a hard I/O latency guarantee.
Hard links count twice conservatively; unexpected non-regular entries are not
followed. Trusted storage paths must not be modified concurrently by operators.

**This is not a filesystem quota.** Concurrent receipt creation, WAL replay and
recovery copying can consume space after admission. Keep separate headroom and
use a dedicated volume or filesystem quota to protect the application's disk.
A full admission budget rejects new work rather than discarding accepted work;
it does not poison the journal merely because a capacity threshold is reached.

A 2 GiB threshold is not a promise of 24-hour outage survival. Size from measured
serialized bytes/second and required outage duration, then include all recovery
and quarantine overhead. Limited space, unbounded outages and never blocking the
application cannot together guarantee lossless telemetry.

## Safe accepted-receipt reclamation

With `receipt_gc: true`, the owner freezes the ID frontier when it opens a replay
snapshot. It tracks the smallest unresolved ID across every batch in that
snapshot. At EOF, it writes a checksummed released-prefix checkpoint no higher
than that unresolved gap, synchronizes the file and parent directory, and only
then removes valid full-acceptance receipts within that prefix.

The next process restores ID allocation above both the WAL maximum and the
checkpoint. Older WAL records at or below that checkpoint cannot be exported
again merely because their receipts were reclaimed. Concurrent new admissions
are beyond the frozen frontier and cannot be swept into an earlier checkpoint.

Quarantine/permanent/partial outcomes retain their payloads. Foreign namespaces,
higher IDs and uncertain temporary files are never blindly removed. Corrupt
receipts or checkpoints fail closed. This is **not** TTL deletion, per-record
arbitrary GC, an exactly-once promise or a physical power-loss qualification.
An unresolved early record can hold back the reclaimable prefix and therefore
increase retained storage; repair the destination rather than deleting state.

The service's HTTP 200 still means local `WriteData + Sync`, not completed
backend storage. A remote response lost before its local receipt is persisted
can still cause a duplicate. A downstream Collector must durably accept into its
exporter queue before acknowledging, without a preceding asynchronous in-memory
batch stage. Test that ownership handoff against the actual backend topology.

## Upgrade and rollback

Generation version 1 remains readable. Once receipt GC publishes a checkpoint,
`generation.json` becomes version 2. **Older binaries intentionally reject that
metadata**, because replaying the directory without understanding its released
prefix can duplicate exports and reuse IDs.

Disabling `receipt_gc` stops future pruning; it does not downgrade metadata or
make that directory compatible with an older binary. Preserve generation, WAL,
receipts and quarantine together. Never edit/delete the checkpoint to make an
old process start. Roll back application routing separately while keeping a
compatible edge binary available to drain pending work. Restoring a backup must
restore the whole coherent owner state, not just the WAL.

## Operations and validation

The protected management `/monitor` includes `otlpStorage` with:

- `admissionRejected`: process-local byte/scan-budget admission rejection count;
- `scanBudgetRejected`: the subset caused by metadata entry/time budgets;
- `acceptedReceiptsReclaimed`: accepted receipt files reclaimed this process;
- `maxStorageBytes`, `storageScanMaxEntries`, `storageScanTimeout` and `receiptGC`:
  configured control values.

Counters reset on restart and are not durable delivery receipts. Monitor actual
filesystem usage/free space and existing OTLP destination outcomes too. Native
Prometheus metrics, per-signal fairness, quarantine expiration, hard filesystem
quotas and production sizing are not introduced by this change.

Run `go test -race -count=3 -shuffle=on ./internal/otlpstate ./internal/controller`
on the supported Unix build. New tests cover healthy plain/gzip restart cycles,
unresolved fan-out gaps, concurrent admission, corrupt metadata/receipts,
checkpoint publication failure, quarantines and large/zero-item admission.
The `OTLP edge acceptance` workflow retains source, toolchain and executable
format/restart evidence. The companion VPS workflow additionally tests the real
Victoria/Collector pipeline, including SIGKILL after edge receipt collection.
No production host or live credentials are used in those campaigns.
