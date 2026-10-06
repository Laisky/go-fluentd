# OTLP edge storage controls

The OTLP owner can act as a persistent edge agent between a local application
and a remote Collector. OTLP itself remains opt-in. An enabled parsed service
configuration now has finite byte/file admission defaults and checkpoint-safe
accepted-receipt GC. These controls do not alter the legacy log journal.

## Configuration and scope

Add the following inside `settings.otlp`, together with the existing listener,
authentication, destinations and separate persistent journal directories:

```yaml
max_wal_bytes: 1073741824       # 1 GiB logical WAL admission threshold
max_storage_bytes: 2147483648   # 2 GiB whole-root admission + reserved future work
max_storage_files: 4096        # all directory entries + reserved future names
storage_scan_max_entries: 4096 # metadata entries per scan; refusal on exhaustion
storage_scan_timeout: 25ms     # combined admission scan time between syscalls
receipt_gc: true               # upgrades generation metadata; read rollback below
```

`max_storage_bytes: 0` or omission selects max(512 MiB, twice `max_wal_bytes`),
capped at 2^50 bytes. Explicit values must be at least `max_wal_bytes` and at most
2^50. Zero never disables accounting. `max_storage_files: 0` or omission selects
4096; explicit values are 16–1048576. Files here means directory entries, including
directories and temporary names, rather than unique inodes. Admission accounts
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

Every admitted but not checkpoint-released ID reserves conservative bytes and
names for its encoded WAL/replay copy, all frozen destination receipts (including
maximum configured response size and validated metadata), atomic publication and
checkpoint writes. This uses a constant-size high-water bound, not a growing
pending-ID map. Accepted IDs behind an unresolved gap retain their reservation.
Explicit `receipt_gc: false` also retains the historical ID upper bound and can
therefore stop admission earlier than the physical receipt size alone suggests.
After restart with retained IDs, new admission returns retryable HTTP 503 until
the first bounded replay snapshot recovers the reservation high-water mark;
existing durable work continues. A fully released prefix resets the high-water
reserve. Raising fan-out/response limits or shrinking capacity may require an
operator to resize the policy; it never authorizes data deletion.

**This is not a filesystem quota.** Reservations are conservative logical byte/
name accounting, not allocated-block or physical-inode guarantees. Filesystem
block rounding, external writers, pre-existing over-budget state and storage
faults still need independent protection. Keep separate filesystem headroom and
use a dedicated volume or filesystem quota to protect the application's disk.
A full admission budget rejects new work rather than discarding accepted work;
it does not poison the journal merely because a capacity threshold is reached.

A 2 GiB threshold is not a promise of 24-hour outage survival. Size from measured
serialized bytes/second and required outage duration, then include all recovery
and quarantine overhead. Limited space, unbounded outages and never blocking the
application cannot together guarantee lossless telemetry.

## Safe accepted-receipt reclamation

Parsed service configuration defaults `receipt_gc` to true; explicit false is
preserved. Programmatic `OTLPJournalConfig`/`OTLPServiceConfig` bool zero values
still mean false, but their omitted capacity settings remain finite. With GC
enabled, the owner freezes the ID frontier when it opens a replay
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

**Defaults change on upgrade:** omitted `receipt_gc` enables checkpoint-safe GC,
and omitted/zero capacity settings no longer allow unlimited retention. An
explicit retention-only configuration must set `receipt_gc: false` and still
provide sufficient finite capacity. Back up the coherent ownership directory
before upgrading and keep a compatible binary available for recovery.

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

- `admissionRejected`: process-local byte/file/recovery/scan-budget admission rejection count;
- `scanBudgetRejected`: the subset caused by metadata entry/time budgets;
- `acceptedReceiptsReclaimed`: accepted receipt files reclaimed this process;
- `maxStorageBytes`, `maxStorageFiles`, `storageScanMaxEntries`,
  `storageScanTimeout` and `receiptGC`:
  configured control values.

`capacity` reports last inventoried `bytes`/`files` (possibly a lower bound on
refusal), `pending_upper_bound` (frontier minus durable released prefix), and
`recovery_ready` (the initial retained-WAL reservation snapshot has completed).
A ready recovery observation does not promise free admission capacity. On
Linux/macOS, `filesystem_available` also qualifies current available-byte and
free-inode observations; unsupported/failed filesystem queries report false,
not a fabricated zero-capacity disk. These observations are not admission proofs.

Counters reset on restart and are not durable delivery receipts. Monitor actual
filesystem usage/free space and existing OTLP destination outcomes too. Native
Prometheus metrics, per-signal fairness, quarantine expiration, hard filesystem
quotas and production sizing are not introduced by this change.

Run `go test -race -count=3 -shuffle=on ./internal/otlpstate ./internal/controller`
on the supported Unix build. New tests cover healthy plain/gzip restart cycles,
unresolved fan-out gaps, concurrent admission, corrupt metadata/receipts,
checkpoint publication failure, quarantines and large/zero-item admission.
`TestOTLPJournalRetainedReceiptsDrainRestoresSameScanCap` specifically admits 32
records with a 16-entry scan cap, then retains 31 real full-acceptance receipts
behind a retryable first record. The next admission must refuse without writing,
advancing identity/checkpoint, changing receipt bytes, or poisoning storage.
After repairing that isolated destination, existing replay reaches EOF, advances
the durable checkpoint, and reclaims all 32 receipts despite the admission cap.
Closing/reopening the same ownership directory with the unchanged 16-entry cap
then admits and delivers a new record with the next ID. Previously accepted
records are not sent again. This is recovery from a retryable prefix gap;
permanent quarantine and uncertain evidence remain outside accepted-receipt GC.

```sh
go test -mod=readonly -race -count=3 -shuffle=on -timeout=90s -run '^TestOTLPJournalRetainedReceiptsDrainRestoresSameScanCap$' ./internal/controller
```

The `OTLP edge acceptance` workflow retains source, toolchain and executable
format/restart evidence. The companion VPS workflow additionally tests the real
Victoria/Collector pipeline, including SIGKILL after edge receipt collection.
No production host or live credentials are used in those campaigns.

The default-policy regressions also cover plain/gzip, small/large payload fan-out,
repeated restart without identity reuse or re-export, exact byte/file headroom
boundaries, concurrent admissions, finite quarantine retention and recovery
before new admission. Sparse logical evidence sizes are used for byte rejection;
no disk-fill experiment is needed.

The paired CI delivery campaigns now use a bounded end-to-end delivery window
and a 256-byte response cap matching their synthetic peers. Both baseline and
candidate receive the same settings. Their results are not directly comparable
to older ACK-window burst measurements: default finite admission intentionally
refuses an unbounded backlog. Capacity-refusal and scan-time-limit regressions
remain separate; functional listener/interoperability fixtures use a one-second
scan budget to avoid qualifying host scheduling latency under race instrumentation.
