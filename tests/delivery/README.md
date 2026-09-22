# Executable-level delivery tests

## Contract first, coverage second

See [CONTRACT.md](CONTRACT.md). The oracle is the producer's immutable event key,
complete submitted fields, and each required sink's independently fsynced ledger.
It never uses application queue lengths, success counters, Go test hooks or
private journal functions as evidence of delivery. One HTTP `200` from an output
is not enough: the protocol peer can reject items, stall, or lose its reply.

Run from the repository root on Linux, using Go 1.27 and Python 3.9+:

```sh
go build -race -mod=readonly -o /tmp/go-fluentd-delivery .
GORACE='halt_on_error=1 exitcode=66' python3 tests/delivery/run.py \
  --binary /tmp/go-fluentd-delivery --artifacts /tmp/delivery-results --seed 127
```

The runner changes test order and deterministic payload/failure schedules using
the recorded seed. It fails on skips, fixture errors, unexpected process exits,
panics, detected races, missing records, altered fields, changed duplicate
payloads, fabricated events, and IDs reused across distinct retained events.
`results.json` records the binary hash, case order and exact assertions. Per-case
artifacts contain producer manifests, submitted HTTP documents, responses,
fsynced sink records, observations, generated configurations and process logs.
They do not contain large WAL files. The fixture salt is not a production secret.

## Executed scenarios

| Area | Independent observation |
|---|---|
| Healthy fanout | Every expected record reaches both sinks, unchanged and once in the failure-free run. |
| Plain/gzip crash | SIGKILL, then a fresh ordinary executable with the same WAL. No extra rotation or repair hook. |
| Immediate crash | Kill immediately after reliable HTTP success, before observing any sink event. |
| Failed/stalled sink | One successful sink cannot retire another sink's obligation. |
| Lost reply | Sink fsyncs and closes the connection without a reply; restart must retry identical data. |
| Invalid reply | HTTP 200 with a missing bulk result is not treated as acceptance. |
| ID recovery | New accepted records after restart cannot share IDs with retained unacknowledged events. |
| Capacity pressure | Queue capacity one, 16 concurrent HTTP producers, 160 accepted events; recovery drains after pressure is removed. |
| Repeated crashes | Four generated sink-failure phases and multiple SIGKILL/restart cycles, then both sinks recover all records. |
| Storage refusal | A file blocks creation of the journal directory; return 503, keep the service alive, then recover after repair. |
| Actual write failure | RLIMIT_FSIZE causes a real filesystem write error, not an injected Go callback. No false acceptance/forwarding. |
| Interrupted append | A failed plain/gzip append must not prevent recovery of earlier successfully accepted records. |
| Settled confirmation | Wait beyond ID-cache TTL and the configured confirmation flush period; restart and a new sentinel verify no continuing replay. |
| Multiline | Kill with a pending head/tail; reconstructed output must contain both original line tokens. |
| Fluent TCP ingress | Independent MessagePack producer sends Message, Forward and PackedForward frames in 37-byte TCP fragments through the real parser/routing/WAL/output path. |
| Negative controls | Invalid signatures/prefilter rejections never count as accepted; deliberately broken ledgers must fail the oracle. |

There are 21 test methods; several contain multiple phases or two sinks. This is
not a claim to exhaust all possible fault interleavings. Normal TCP ingestion
has no durable acceptance reply and its test deliberately makes no
crash-immediately-after-send promise.

## Reliable HTTP acceptance is explicit

For an HTTP receiver plugin, set:

```yaml
require_durable_ack: true
```

This opts into a new contract: HTTP success waits for the journal write and
`Sync()` to complete. Before that point, pipeline pressure must wait rather than
bypass persistence. Storage errors and pre-persistence filter rejection return
503. A client timeout/disconnect means **unknown outcome**, not proof of
nonacceptance; retries may duplicate an event. The flag defaults to false to
preserve the legacy best-effort/queue-acceptance contract. Per-record sync has an
intentional throughput/latency cost; no performance improvement is claimed.

Reliable acceptance is local ownership, not immediate downstream delivery. The
configured post-persistence filters/routes must preserve the records of interest,
all required destinations must be enabled, and explicit discard/dry modes must
be off. Retrying requires disk space and eventual healthy reachable sinks.

## Reproduce the old behavior

Build the old revision `dc768b2ec84625f71bc9119fa20641d0673160c3` in a separate
worktree with its original dependency version, then run:

```sh
python3 .scripts/verify_delivery_regressions.py \
  --before /tmp/go-fluentd-before --after /tmp/go-fluentd-delivery \
  --artifacts /tmp/delivery-red-green
```

Both binaries must pass the same healthy/invalid-input controls. The old binary
must produce the three specific external failures (false storage acceptance,
retained-ID collision and first-pass recovery omission); the new binary must
pass all five selected tests. Compilation/fixture errors and whole-run timeouts
are not accepted as reproductions. A bounded missing-delivery assertion is a
liveness failure, not a claim that the old record was permanently deleted.

The public journal recovery tests additionally protect complete-prefix recovery
from an interrupted newest append, retain a byte-for-byte `.incomplete` evidence
file, and reject arbitrary corruption. Older-segment corruption, bad checksums,
failed evidence retention, and unrecognized damage still fail closed. Operators
must inspect/archive `.incomplete` files; they are not silently erased.

## Limits

These are actual processes, sockets, fsync calls and kernel-enforced write limits,
but not an actual Elasticsearch/Kafka cluster. SIGKILL does not discard the OS
page cache or simulate host power loss. File/directory sync relies on the
filesystem/storage honoring those operations. No exactly-once, all-platform,
all-format corruption repair, unbounded backlog, counter-wrap or arbitrary
configuration guarantee is inferred. TCP write completion is not a downstream
durable ACK. Coverage percentages are not the delivery acceptance criterion.
