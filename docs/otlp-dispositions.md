# Durable OTLP terminal dispositions

## Scope of this increment

`internal/otlpstate` is a per-destination terminal-result store and guarded send
operation. It is **not yet connected to the producer, receiver or exporter**.
No OTLP endpoint is enabled by this increment. See [the OTLP plan](otlp.md).
The future adapter must explicitly distinguish `Result.Quarantined` from
`Outcome.Kind == Accepted`: successful quarantine is never successful delivery.

An OTLP partial-success response identifies rejected counts, not the rejected
subset. Re-exporting the entire request can duplicate already accepted metrics.
The [OTLP specification](https://opentelemetry.io/docs/specs/otlp/) prohibits that
retry. Permanent and invalid responses also need an explicit non-success outcome
instead of being sent through the application's ordinary replayable-failure path.
The protocol parser and its classification remain in `library/otlpwire`.

## Guarded operation

`Store.Do(ctx, key, envelope, send)` runs under a per-key striped lock:

1. Read and verify any existing durable terminal receipt. If present, return its
   original outcome without invoking the destination callback.
2. Otherwise invoke the callback with an owned copy of the immutable payload.
3. Return accepted/retryable/transport outcomes without creating a terminal
   record. Their retry and WAL-ACK policy still belongs to the application.
4. For partial, permanent or invalid outcomes, write the original envelope,
   bounded peer response, rejected count and diagnostic into a private file.
   Synchronize that file, atomically link it to a no-replace final name, then
   synchronize the directory before returning `Quarantined: true`.

The terminal result is retained even if the context was canceled while the
callback ran. A classification/storage error after a response blocks this Store
instance; later calls cannot silently send the request again. A transport error
before a classified response remains a transport error, not a quarantine entry.
The callback must honor its context and must not recursively call Store methods.
Close waits for in-flight callbacks before releasing directory ownership.

The key consists of a stable, opaque destination ID, journal namespace and local
record ID. Its length-delimited hash becomes the filename. The record binds that
key to signal, content type, known item count and exact original uncompressed
payload. Reusing a key with changed content fails closed. The same payload sent
to two destinations has independent outcomes; one rejection cannot suppress the
other destination. A changed endpoint/credential configuration must not silently
change the logical destination ID and cause old rejected requests to be resent.

Do not use credential-bearing URLs as destination IDs. Do not record Authorization
or arbitrary peer headers in diagnostics. Unknown payload fields are retained as
bytes; this store neither parses OTLP nor performs schema/temporality conversion.
The producer must construct the Envelope from an already validated `otlpwire.Request`.

## Filesystem and recovery contract

Provision an existing, application-owned persistent directory, preferably mode
0700, and make its creation durable before calling `Open`. Open rejects a missing,
empty or symlink directory and obtains an exclusive advisory file lock. The lock
inode `.otlp-disposition.lock` is never unlinked, including on Close. A process
crash releases the OS lock. Unix locking is implemented; the unsupported-platform
stub refuses to open rather than silently proceeding without ownership.

Final `*.json` records and `.pending-*` evidence are mode 0600. The final record
contains versioned JSON and a SHA-256 digest of the embedded entry. The checksum
is accidental-corruption detection, **not** authentication against a malicious
writer. The directory must be trusted and must not be renamed, replaced or edited
while the store is open. No protection against a hostile local filesystem owner
or unsupported remote-filesystem locking is claimed.

A failed write/sync/link keeps temporary evidence. If no final receipt exists but
that identity has a `.pending-*` file, a fresh process refuses automatic export
with `ErrUncertain`; even a partial temp record is not treated as a cache miss.
If a valid final record survived a crash after linking, Open's fresh directory
Sync establishes that surviving publication before it can be used. Redundant temp
names beside a valid final receipt may survive; lookup validates the final record.
Corruption, mismatched identities, symlinks and oversized receipts are errors,
not absence. Missing unrelated destination records still remain independent.

After an uncertain storage error, stop the exporter, preserve both its WAL and
this directory, repair the storage, and inspect the evidence. Do not remove a
receipt or orphan merely to make export resume: that can duplicate delta metrics.
There is no automatic forensic repair, receipt TTL, deletion or compaction in
this increment. Lifecycle/retention must be coordinated with all replayable WALs
before any future cleanup is added. Disk usage and file counts need monitoring.

No software can recover a reply that was never persisted: a crash between the
remote response and creating local evidence, a CreateTemp failure followed by a
crash, or physical loss of un-synchronized filesystem state leaves an ambiguous
outcome. This mechanism reduces known-terminal replay; it does not establish
exactly-once delivery or eliminate the remote-send/local-commit crash window.
Storage must honor file and directory Sync. SIGKILL tests leave page cache intact.

## Resource bounds

Default limits are 4 MiB per request payload and 1 MiB per captured response.
Limits are explicit and validated; diagnostics are at most 4 KiB. An oversized
peer response must be captured as a bounded prefix with `Truncated: true` and
classified as invalid by the exporter. Do not turn it into successful acceptance.
The full original envelope remains retained even for partially rejected requests.
There are 64 lock stripes, no unbounded in-memory receipt map, and no whole-receipt
TTL scan. JSON/base64, validation and owned copies still allocate per operation.
These are per-entry bounds, not global memory, connection or disk quotas.

## Verification

```sh
go test -mod=readonly -count=1 -timeout=90s ./internal/otlpstate
go test -mod=readonly -race -count=3 -shuffle=on -timeout=180s ./internal/otlpstate
python3 .scripts/verify_otlp_state_contracts.py --artifacts /tmp/otlp-state-controls
```

Public tests cover all three signal labels, both payload encodings and three
terminal classifications; exact retained response bytes; two reopen cycles;
independent destinations; concurrent same-key calls; canceled work; concurrent
Close; input identities; and corrupted receipt files. Transport-error and accepted
controls remain nonterminal. Empty and nil protobuf byte slices have the same
wire identity, not a false content conflict.

Linux subprocess tests use an independent HTTP peer, actual SIGKILL/reopen, a
second-process ownership check, and RLIMIT_FSIZE=96 to cause an actual partial
record write. The peer call count must stay at one after reopening a terminal or
uncertain receipt. These are **guard/store consumer-process tests**, not a
configured go-fluentd OTLP pipeline or real Collector interoperability test.
Classification is explicitly supplied by the test consumer; otlpwire tests the
protocol classifier independently. No signal semantic validation is inferred
from the byte-preservation tests.

Per-instance filesystem fault seams additionally block file Sync, link and
directory Sync to check receipt ordering; injected EIO at those operations must
never produce successful quarantine. Four disposable-source mutants remove a
barrier, ignore persistence failure or resend known terminal results. Each must
fail its named assertion while the independent transport-error control passes.
Compilation failures, panics, timeouts and skipped tests do not count as evidence.

## Next integration gate

Wire a distinct terminal/quarantined result into the producer's per-destination
accounting. Preserve the request until every required destination is durably
resolved; do not feed quarantine into delivered counters or ordinary automatic
replay. Test one accepted destination, one retryable destination and one rejected
destination together across restart, including a failed disposition write.
Then implement the OTLP/HTTP adapters, YAML configuration and independent
Collector acceptance. This increment does not change any legacy application path,
ACK semantics, journal format, dependencies or the original README diagram.
