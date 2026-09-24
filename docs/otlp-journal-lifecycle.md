# OTLP journal lifecycle

This increment adds `controller.OTLPJournal`, an owner for the existing OTLP
receiver, producer, exporter and disposition components. It does **not** register
YAML, mount a production listener, or certify Collector interoperability. The
application's configured OTLP feature remains incomplete.

## Ownership and identity

Provision an empty private directory on persistent storage. Its creation and
parent directory must already be durable. `OpenOTLPJournal` exclusively owns the
whole directory through the disposition store's persistent lock inode. It creates
`wal/` and `generation.json`, synchronizes the generation file and root directory,
and retains the random 256-bit namespace across restarts. Existing nonempty
storage without valid generation metadata is rejected rather than adopted.
Partially initialized storage needs inspection; initialization never deletes it.
Generation files and the WAL subdirectory cannot be symlinks. Parent components
and local users are trusted, not a hostile-filesystem security boundary.

The frontier is recovered from both retained data and ACK records using the
locked go-journal implementation. Admissions serialize allocation and writing;
IDs do not restart at one after acknowledged segments are cleaned up. Integer
exhaustion is an explicit admission error. Keep the generation, WAL, and receipts
together. Deleting/restoring only part of this ownership domain is unsupported.

Only the dedicated `otlp_delivery` wrapper enters this WAL. Replay verifies its
outer/inner IDs and generation before exporting. A wrong wrapper, changed
namespace, corrupt record, or uncertain disposition fails closed and blocks new
admission. The existing tag/message log journal and its producer are unchanged.

## Admission and release

`Admit(ctx, request)` is directly assignable to `otlphttp.Admission`. It freezes
the configured logical destination IDs, stores the original decoded request
bytes, and calls `WriteData` followed by `Sync`. It returns nil only after that
barrier succeeds. An in-memory enqueue never constitutes acceptance. Waiting for
serialized admission is cancelable and happens before copying the request.

The receiver may return an error after a completed write if its context expires.
That is an unknown outcome, not a rollback; the journal still recovers the record.
An append or Sync failure poisons the owner rather than permitting later false
success or cleanup. A restart must inspect/recover the retained files normally.

Required peers come from each saved admission plan. Removing a required peer
blocks that record before any peer call; adding a peer does not retroactively
create a destination obligation. `OTLPProducer` records acceptance and quarantine
separately. The release callback performs `WriteId` **and** `Sync` only after every
required destination has a durable result. A quarantined record can release WAL
ownership because the disposition retains its original payload; it is not counted
as fully delivered.

## Bounded recovery

`ReplayBatch(ctx)` loads at most `ReplayBatch` pending envelopes. Before advancing
the legacy iterator it rewrites and synchronizes each record in the active WAL.
Thus EOF cleanup cannot delete the only durable copy of unresolved data. A failed
replacement write stops the owner and leaves the original segment recoverable.

Pausing a batch retains the frozen snapshot and cursor. The next batch does not
rotate and restart that cursor: otherwise a failing first message could starve
unread messages or repeatedly copy already processed data. New admissions are
written to the active segment and appear in the next snapshot. A completed
snapshot means EOF was reached, **not** that every destination delivered.

Automatic journal flush/rotation timers use the maximum duration; this owner
explicitly synchronizes and rotates. The ordinary segment preallocation hint is
1 MiB. It is not set to an enormous size to disable rotation. Only the owner
controls snapshot rotation, with no periodic business-message map sweep.

`Run(ctx)` drives bounded batches with a minimum pause of `ReplayInterval` between
calls, including after retry exhaustion. There is no unbounded in-memory backlog
map or timer per message. Same-record durable receipts suppress completed peer
calls. The exporter additionally honors its per-signal Retry-After state. Both
schedules are process-local and do not survive restart; a new process can issue
its first attempt immediately. Callers should not run a competing manual replay
scheduler, though concurrent batches are serialized.

`Close` first cancels active operations, then waits before closing the WAL and
receipt store or releasing either ownership lock. Destination callbacks must
honor context cancellation. Close can wait for a blocked filesystem operation;
no context can cancel a kernel Sync already in progress. A final Sync failure is
returned, and pending data is never declared delivered merely because Close ran.

## Configuration and bounds

The component accepts these fields, independently of any future YAML mapping:

| Field | Default | Meaning |
|---|---|---|
| `Directory` | required | Existing private persistent ownership directory |
| `Compress` | false | Gzip for journal files; unrelated to HTTP wire gzip |
| `Limits` | disposition defaults | Per-envelope and response evidence limits |
| `MaxWALBytes` | 256 MiB | Admission threshold on WAL logical file sizes |
| `ReplayBatch` | 64 | At most this many pending envelopes per replay call |
| `ReplayInterval` | 1 second | Minimum delay between scheduled batches |

Admission checks the current WAL logical sizes plus a conservative encoded-record
reserve before writing. `MaxWALBytes` is **not a hard filesystem quota**, does not
count preallocated physical blocks or receipt files, and does not cap copies
needed to preserve unresolved records during recovery. Operators need filesystem
capacity monitoring and headroom. Accepted/quarantined receipts still have no
compaction, TTL or total disk quota; do not delete them to bypass a limit.

The admission check scans WAL directory metadata; receipt misses also inherit
the current disposition store's directory scan. Recovery copies and per-record
Sync operations have material CPU, allocation, disk and inode costs. No throughput,
constant-time disk lookup, or performance-neutrality claim is made. One replay
batch processes peers sequentially; a slow destination can delay that batch while
new admissions can still append through the independent WAL gate.

## Behavioral acceptance

Tests use the public owner with actual journal and receipt files, saved caller
payloads, and independent HTTP peers. They cover restart identity, cleanup,
concurrent admissions, wrong/missing generation, frozen destination obligations,
capacity rejection, cancelable waits, cadence and active-call shutdown.

Six HTTP integration scenarios use the real receiver/owner/exporter for logs,
metrics and traces with plain/gzip journals. Partial rejection remains separate
from delivery and successful peer receipts survive reopening. Two Linux process
scenarios (plain/gzip) obtain a real HTTP 200, process mixed peer outcomes, and
execute three SIGKILL/reopen phases. Expected peer calls are A=1, B=2, C=1; original
request bytes remain unchanged. These are consumer processes, not CLI/YAML or an
independently running Collector. Existing wire tests cover protobuf/JSON and
wire gzip; the new process scenario specifically uses OTLP JSON logs.

Deterministic per-instance write/Sync seams supplement real disk/process tests.
They assert no admission before Sync, no network call before a synchronized
replay copy, and blocked operation after storage errors. Five disposable-source
negative controls omit admission Sync, omit the replay copy, restart the cursor,
ignore a namespace mismatch or hide an append error. Named assertion failures
are required; compilation errors, panics, skipped tests and timeouts do not count.

```sh
go mod verify
go test -mod=readonly -count=1 -timeout=180s -run '^TestOTLPJournal' ./internal/controller
go test -mod=readonly -race -count=3 -shuffle=on -timeout=180s ./...
python3 .scripts/verify_otlp_journal_contracts.py --artifacts /tmp/otlp-lifecycle-controls
```

SIGKILL preserves page cache. Sync depends on storage honoring its contract, and
a crash between a remote response and local receipt persistence can still cause
duplicates. This is not exactly-once or physical-power-loss qualification.

Next: wire the owner and handler into a dedicated secured HTTP listener and YAML,
then validate the ordinary executable against an independent Collector, including
storage refusal, overload, multi-destination recovery and operator examples.
