# OTLP destination accounting

## Implemented boundary

`controller.OTLPProducer` connects the disposition store to per-destination
accounting and an explicit journal-release callback. It is a **component**, not
an enabled OTLP receiver/exporter. No routes or YAML registration are introduced,
and the legacy log `Producer` remains unchanged. The next increment must wire
this component into a dedicated OTLP pipeline and test actual Collector delivery.

## Admission plan and journal ownership

`Plan(namespace, id, envelope)` takes an owned snapshot of the currently required
logical destination IDs. Persist `record.JournalData()` and successfully `Sync()`
before acknowledging ingress or contacting peers. This method does not perform
that persistence for the caller. Its `otlp_delivery` wrapper is for a dedicated
OTLP journal; **do not feed it to the legacy tag/message replay path**.

Recover with `OTLPRecordFromJournal`. The outer and inner record IDs must agree.
The required destinations come from the saved admission plan, not today's
configuration. A newly added peer does not retroactively receive old records;
a missing required peer blocks processing before any other peer is contacted.
Explicit migration is necessary to change an existing obligation. The caller
must never edit an admitted plan or reuse a destination identity for a different
logical backend. Namespace must be persisted with the WAL and distinguish fresh
journal generations that could reuse numeric IDs.

Payload bytes remain opaque. The receiver must construct Envelope from a valid
`otlpwire.Request`; this component does not decode signals, infer rejection
subsets, aggregate metrics, or validate semantic conventions. Its structural
limits are not a substitute for receiver admission limits.

During replay, write pending replacements before requesting the next record or
EOF cleanup, following [go-journal recovery](https://github.com/Laisky/go-journal).
`Process` is not a replay iterator or background scheduler.

## Durable destination results

`Store.DoDelivery` preserves both accepted and terminal destination results using
the existing file-Sync, no-replace publication and directory-Sync ordering.
The original `Store.Do` retains its terminal-only policy for fresh results.
Both operations honor already recorded receipts without sending again.

| Destination outcome | Durable disposition | Processing result |
|---|---|---|
| Valid full acceptance | Version 2 acceptance receipt | Resolved; fully accepted for this peer |
| Partial/permanent/invalid result | Version 1 quarantine receipt | Resolved; **not** fully delivered |
| Retryable response | No completed receipt | Pending; cannot release the journal |
| Transport failure | No completed receipt | Pending error; cannot release the journal |
| Receipt persistence failure | No successful durable result | Error; block further use of that Store instance |

A saved acceptance or quarantine suppresses that destination's callback across
restarts, even when another peer is still retrying. Version 1 terminal receipts
remain readable. Old terminal-only binaries reject version 2 instead of silently
ignoring it: do not assume rollback compatibility.

`Process` performs one sequential, bounded pass over up to 64 required peers.
Different records can run concurrently. Same-destination sends are serialized by
the store's key stripes. Callbacks must obey context cancellation; scheduling,
backoff, admission control and queue management belong to the eventual adapter.
A classified remote result is persisted even when cancellation arrives after it.

## Resolved is not delivered

`OTLPDeliveryReport.Resolved` requires every peer to have a durable acceptance or
quarantine. `FullyDelivered` requires full acceptance from **every** peer.
Quarantine retains the whole original envelope and diagnostic in separate
storage; it never means the downstream accepted every item.

The release callback is invoked only when all destinations are resolved. It must
idempotently execute the correct journal `WriteId` and `Sync`, and return any
error. `JournalReleased` becomes true only after that callback succeeds. It means
local WAL ownership can end, not that quarantined telemetry was delivered.

A failed journal release can be retried without resending durable peer results.
Callbacks may be invoked more than once, including concurrent Process calls, so
the release implementation must be concurrency-safe and idempotent. This API
cannot detect a callback that falsely reports its synchronization succeeded.

Counters are process-local observations of **destination envelopes**, not items
or durable lifetime totals. New accepted receipts increase Accepted; new terminal
receipts increase Quarantined. Replays increase ReplayHits, not either outcome
counter. Retryable and Blocked count processing observations. A process crash can
lose counter updates even while its receipt survives; use receipts for recovery,
not these counters. Counter snapshots are independent atomic reads.

## Verification and limits

```sh
go test -mod=readonly -count=1 -timeout=180s ./internal/otlpstate ./internal/controller
go test -mod=readonly -race -count=3 -shuffle=on -timeout=180s ./internal/otlpstate ./internal/controller
python3 .scripts/verify_otlp_accounting_contracts.py --artifacts /tmp/otlp-accounting-controls
python3 .scripts/verify_otlp_state_contracts.py --artifacts /tmp/otlp-state-controls
```

Public controller tests use actual journal files, immutable admission plans,
changed configurations, mixed destination outcomes and failed release callbacks.
Accepted-receipt tests cover all three signal labels and both encoding labels,
reopening, caller-owned copies, cancellation and concurrency. They supply opaque
payloads and explicit classifications; they are not new OTLP semantic tests.

Linux consumer-process tests persist a plan, obtain accepted/retryable/partial
results from independent HTTP peers, SIGKILL the consumer, retry only the pending
peer, fail journal release, SIGKILL again, and finally release without another
export. Plain and gzip journals are both exercised. These are real producer/store/
journal process tests, **not the configured go-fluentd executable or Collector**.

Five disposable-source negative controls must fail assertions: omit accepted
persistence, resend saved acceptance, count quarantine as acceptance, release
with a pending destination, or report quarantine as full delivery. Independent
positive controls must still pass. Existing four storage-barrier mutants remain.
Build failure, panic, race warnings, timeout and missing/skipped tests do not count.

Every resolved destination currently retains the full envelope and response.
This adds copies, file/directory synchronization, disk consumption and inodes;
there is no throughput or performance-neutrality claim. No receipt compaction,
TTL or disk quota is implemented. The existing pending-evidence lookup uses a
flat-directory glob on a missing final receipt; it can scan that directory and
needs measurement before large-backlog deployments. There is no unbounded
in-memory receipt index, but that does not imply constant-time disk lookup.

Preserve both WAL and disposition storage. Do not remove receipts merely to
resume exports. A crash after a remote response but before any durable evidence,
or losing storage, can still cause duplicates. SIGKILL does not simulate physical
power loss. See [disposition recovery](otlp-dispositions.md) for uncertainty and
operator rules. Receiver/exporter wiring, bounded scheduling, persistent namespace
provisioning and independent Collector acceptance remain open integration gates.
