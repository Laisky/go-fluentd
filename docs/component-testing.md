# Component behavior tests

## Scope and baseline

The baseline is merged PR #6, `d97a5175e3287bbd9282be06425b5c6ea2285525`.
The component work is in PR #8. The initial Go 1.27.1 run measured **22.1%**
statement coverage. The expanded suite measured **67.6%** locally using the same
`go tool cover -func` aggregate. Counting all profile blocks, including package
initializers, gives 22.0% to 67.3%; the CI gate uses this latter denominator. No production packages or generated files are
excluded from these totals. Exact percentages can vary slightly with scheduling.

| Package | Baseline | Expanded suite |
| --- | ---: | ---: |
| `internal/acceptorfilters` | 0.0% | 81.4% |
| `internal/controller` | 6.0% | 52.6% |
| `internal/monitor` | 0.0% | 100.0% |
| `internal/postfilters` | 0.0% | 95.2% |
| `internal/recvs` | 29.2% | 63.0% |
| `internal/senders` | 39.7% | 81.8% |
| `internal/tagfilters` | 0.0% | 81.5% |
| `library` | 59.0% | 71.5% |

## Behavior contracts

- **Admission and routing:** async/sync receiver bindings, recovered-ID lower
  bounds, unique parallel receiver IDs, default queue capacities, filtering
  order, re-entry, tag support, cached pipeline creation and recovery after a
  pipeline creation error. Existing explicit overload/bypass policies remain
  visible; these tests do not silently redefine them as durable acceptance.
- **Parsing and normalization:** string/byte equivalence, timestamp formats and
  time zones, regex capture types, required fields, JSON-object-only parsing
  without partial mutation, field inclusion/exclusion, canonical key collisions,
  length limits, tag rewriting, row keys and placeholder expansion.
- **Concatenation:** worker/tag isolation, orphan lines, size and idle flushes,
  normal input close, blocked-output cancellation, all original `ExtIds`, and
  no acknowledgement of a tail before the combined message succeeds. Synthetic
  time tests use `testing/synctest` rather than waiting five real seconds.
- **Receivers:** HTTP validation and actual-body limits, cancellation while
  backpressured, pooled-message reset, Kafka JSON/plain decoding, Syslog field
  normalization and invalid timestamps, Fluent Message/Forward/PackedForward
  frames, malformed entries and idle connection cancellation.
- **Senders:** HTTP/Elasticsearch batch preparation, gzip integrity, status and
  item failures, retries, request cancellation and partial-batch input close;
  Fluent TCP framing/flush/reconnect; Kafka serialization failures, stale-payload
  prevention, bounded send attempts, producer replacement and resource closure;
  stdout's explicit success/failure policy.
- **Producer:** all required senders must finish before acknowledgement,
  any failed sender prevents commit, same-ID replay instances stay distinct,
  and a cached blocked sender follows the configured discard policy safely.
- **Journal:** actual temporary files, plain and gzip operation, synchronous
  replay replacement, cancellation/error ownership, malformed-record retention,
  acknowledgement retries, ID recovery, explicit bypass, and acknowledgement
  routing back to the original journal after downstream tag rewriting.
- **Monitoring and lifecycle:** concurrent metric registration/snapshots,
  serialization failure status, pipeline creation concurrent with metrics,
  graceful HTTP-server shutdown and request draining. Subprocess tests isolate
  fatal goroutine failures; their child-process statements are not credited to
  the parent coverage profile.

## Confirmed defects repaired

Fail-first tests exposed queue construction before default normalization; a
missing dispatcher unlock after spawn failure; parser double delivery, skipped
add rules, inconsistent time-zone parsing and partial JSON mutation; shared
concatenation state, early tail acknowledgement and missing final flush; unsafe
concurrent monitoring; resurrected/incorrectly rewritten fields; malformed
routing metadata panics; broken template substitutions; stale pooled confirmation
IDs; malformed Fluent input panics and cancellation leaks; HTTP/ES partial-batch
loss; Kafka stale-payload false success and producer leaks; Syslog same-name field
loss; and a panic on ordinary HTTP-server shutdown.

A cross-component test additionally found that retagged messages wrote their
acknowledgements into the destination journal rather than their source journal.
`FluentMsg.JournalTag` now preserves local acknowledgement provenance through
routing and replay. It is deliberately excluded from JSON and MessagePack. The
on-wire and stored record schemas remain unchanged. Confirmations without a
journal owner retain the existing explicit skip-dump fallback to the routing tag.

Passing controls include valid timestamps and frames, positive filter routes,
successful transport requests, already-acknowledged replay suppression, distinct
replay instances, and both unchanged-tag and rewritten-tag journal paths.
A gzip test fixture was corrected to call `Sync()` before reading buffered ID
files; its initial unflushed read was not counted as a production defect. The old
HTTP test's shared router and fixed server lifetime were replaced by a test-local
`httptest` server so repeated/random-order execution remains valid.

## Reproduction and acceptance

Initial coverage: GitHub Actions run `35770356346`.
Core fail-first behavior/race reproduction: run `35771589191`.
Subsequent local fail-first checks used the exact public source and Go 1.27.1
exported by run `35771684744`. The initial remote test commit preserves the core fail-first state. The final
source changes are accepted with an isolated regression-reversion check that
requires 18 named failures on old implementations and nine passing positive
controls; compilation errors and timeouts do not count as reproductions.

```sh
go version
go mod verify
go build -mod=readonly ./...
go vet -mod=readonly ./...
go test -mod=readonly -count=1 -timeout=180s -coverprofile=coverage.out ./...
python3 .scripts/check_component_coverage.py coverage.out
go test -mod=readonly -race -count=5 -shuffle=on -timeout=180s ./...
go test -mod=readonly -race -count=10 -shuffle=on -timeout=180s \
  -run 'Test(Regression|Component)' ./...
git diff --exit-code -- go.mod go.sum
```

The permanent CI runs coverage, race detection and ten randomized rounds of
component/regression tests. Coverage floors in the script prevent regression;
coverage is not a proof of correctness. Normal CI has read-only permissions.
Temporary diagnostic/import workflows are not part of the final change.

## Boundaries

The suite uses local HTTP servers, `net.Pipe`, temporary files, controlled
transport implementations and a fake Sarama producer. It does not qualify a real
Kafka cluster, Syslog UDP/TCP deployment, Elasticsearch cluster, physical
power-loss recovery, or the deployment-owned MooseFS runtime. Application/CLI
configuration assembly and full-process shutdown under every overload condition
are not exhaustively covered. Cancellation does not promise to deliver or
acknowledge unfinished work; normal input closure is tested separately.

TCP Flush is not a downstream durable ACK. Whole-batch retries can duplicate
records, explicit lossy modes remain lossy, and the system is not exactly-once.
The existing two-generation TTL design is retained; no unmeasured throughput or
memory improvement is claimed.
