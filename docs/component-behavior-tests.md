# Component behavior and regression verification

This expands the Go 1.27 migration in #6. The starting source is master
`d97a5175e3287bbd9282be06425b5c6ea2285525`. Dependencies and journal disk formats
are unchanged. Tests assert output contents, identity, acknowledgement counts,
error behavior, routing and cancellation rather than merely executing methods.

## Coverage

Measured on Go 1.27.1, Linux amd64 with `-race -covermode=atomic`.
The aggregate below weights **every statement in the profile**, including
uncovered startup code and generated codecs; it is not an average of percentages.
The CI checker uses that same calculation (the named-function report from
`go tool cover -func` can have a slightly different aggregate).

| Package | Before | After | Enforced minimum |
| --- | ---: | ---: | ---: |
| acceptorfilters | 0.0% | 78.3% | 77% |
| controller | 6.0% | 69.4% | 68% |
| monitor | 0.0% | 100.0% | 99% |
| postfilters | 0.0% | 93.8% | 93% |
| recvs | 29.2% | 74.4% | 73% |
| senders | 39.7% | 84.0% | 82% |
| tagfilters | 0.0% | 85.9% | 85% |
| library | 59.0% | 72.4% | 71% |
| Whole profile | 22.0% | 74.5% | 74% |

A few concurrent error-path counters vary slightly between executions; the
floors intentionally allow this small margin. Do not lower a floor merely to
make CI pass. New behavior needs assertions even when an overall floor passes.

77 new top-level `TestBehavior*` tests contain 136 leaf scenarios. Together with
the earlier regression/legacy tests, a full run passes 240 test nodes, including
117 top-level tests, with no skipped tests. Three fuzz targets exercise message
codec round trips, template literal preservation and Kafka JSON input. These
counts exclude extra generated fuzz inputs and the three Python CI-checker tests.

## Contracts covered

| Component | Behaviors and failure controls |
| --- | --- |
| Receive/acceptor | Fresh pooled state, unique counter wiring, single/Forward/PackedForward records, malformed envelopes, Kafka JSON/raw ownership, HTTP signatures/timestamps/env/body limits and cancelled handoff, syslog field transforms and actual datagram parsing |
| Acceptor filters | Tag acceptance, empty messages, Spring first-match and re-entry, Spark byte/string filtering, sequential stages, separate synchronous/asynchronous outputs and real default capacities |
| Tag filters | Named regex capture, required fields, transactional JSON merge, independent Add/time processing, string/byte timezone equivalence, stable hash affinity, scoped multiline state, constituent-ID ownership, size/idle/closed-input flushing and cancellation |
| Postfilters | Normalization is idempotent, deleted keys stay deleted, normalized-key collisions are deterministic, include/exclude/template behavior, caller-owned config slices remain intact, invalid routing/BigData messages have one explicit disposition |
| Dispatcher/producer | Spawn failure does not hold the registry lock, one pipeline per tag, fan-out waits for all senders, mixed failure does not commit, distinct instances sharing an ID remain distinct, bounded-channel policies, commit backpressure cancellation |
| Senders | Full/partial/closed-input batches, retry success/exhaustion and exact outcomes, gzip/response ownership, malformed/contradictory ES results, real Sarama mock-broker wire exchange, no reuse of stale Kafka payload after JSON failure, stdout policies; earlier Fluent TCP regressions remain active |
| Journal | Actual data and ACK writers, primary plus ExtIds, plain/gzip round trips on temporary files, only unacknowledged records replay, persistence/bypass routing, maintenance and bypass cancellation; earlier rewrite-before-cleanup/error tests remain active |
| Monitoring/server/config | Concurrent metric registration and requests, independent response buffers, invalid metric JSON returns 500, factory activation/environment wiring, configured filter chain, expected HTTP shutdown and active-request draining |
| Helpers/codecs | Missing placeholders, bytes/case transforms, nested fields, ordered Add/remove operations, regex/flatten/environment behavior, pooled Fluent frames, encoder errors, strict existing timer boundary and fuzzed round trips |

Integration tests include configured acceptor filters -> parser -> dispatcher ->
postfilters, and parser -> dispatcher -> producer -> stdout -> commit. The journal
writer/replayer additionally uses real temporary files and the pinned backend.

## Confirmed fixes

Fail-first tests exposed message mutation and lifecycle errors, not only low
coverage. Fixes include stale/missing template substitutions, normalization
resurrecting old keys, include-list backing-array mutation, invalid routing
panics, double processing of unsupported parser tags, failed JSON partially
changing a message, Add depending on timestamp configuration, inconsistent
byte/string timezone parsing, and premature confirmation of multiline tails.

Receiver tests exposed malformed Fluent input panics, input-buffer aliasing,
pooled ExtIds leaking to new messages, idle Fluent reads surviving cancellation,
cross-tag receiver concatenation, syslog same-key rename loss and partial-bind
socket leaks. Lifecycle tests also exposed syslog Boot failure calling a nil
cancel function, blocked handoffs and uncancellable retry sleeps.

Senders now share a worker-local batch lifecycle that drains a partial batch when
its input closes, preserves the immediate first-message behavior, reports one
terminal result, and never retries a stale payload after serialization fails.
HTTP requests receive cancellation. A valid ES `filter_path=errors` response is
still accepted; supplied item statuses are checked for contradictions.

Dispatcher spawn failures release their lock. Producer commit backpressure can
be cancelled. Monitor registration and per-request response encoding are safe
under concurrency. Parser/LB shutdown propagates closure of owned worker inputs;
shared downstream channels are not closed. Journal maintenance and bypass workers
can stop while waiting. Actual buffer capacities match validated defaults.
Normal `http.ErrServerClosed` is not a panic; active requests get a bounded grace
period using a fresh shutdown context, not an already-cancelled one.

## Reproduction and false-positive controls

The branch preserves test-first and fix commits. The first test-only checkpoint
can be run with `go test -count=1 -run TestBehavior` in the relevant package before
the first repair commit. Later lifecycle tests follow a behavior-preserving
extraction of the blocking loops so tests can observe completion directly.
The `reproduction-evidence.txt` excerpt retains actual failing assertions and
race reports from these checkpoints. Full pre-fix suite failure is expected.

Positive controls protect existing policy: unsupported tags bypass a parser once;
valid regex/JSON/signatures still work; reliable versus explicit lossy sender
queues keep their existing distinct policies; successful operations have exactly
one result; duplicate journal copies do not consume the shared committed-ID
membership; `errors:false`-only ES responses remain valid.

Test defects are not counted as production bugs. These included a missing Spark
config field in a new fixture and an incorrect type assertion on an integer
metric. The old HTTP test's package-global engine registered the same route on
repeated runs; it now uses a per-test `httptest.Server`. The old Fluent test now
uses a pre-bound ephemeral listener and joins its receiver instead of sleeping
and relying on a fixed port. No race/vet checks or tests were disabled.

## Repeatable verification

```sh
go mod verify
go build -mod=readonly ./...
go vet -mod=readonly ./...
go test -mod=readonly -count=1 -timeout=180s ./...
go test -mod=readonly -race -shuffle=on -count=10 -timeout=180s ./...
go test -mod=readonly -race -covermode=atomic -coverprofile=coverage.out -count=1 -timeout=180s ./...
python3 .scripts/check_coverage.py coverage.out
python3 -m unittest discover -s .scripts -p 'test_*.py'
go test -mod=readonly ./library -run '^$' -fuzz '^FuzzBehaviorFluentMessageRoundTrip$' -fuzztime=5s -parallel=2
go test -mod=readonly ./library -run '^$' -fuzz '^FuzzBehaviorTemplateLiteralSafety$' -fuzztime=5s -parallel=2
go test -mod=readonly ./internal/recvs -run '^$' -fuzz '^FuzzBehaviorKafkaJSONObject$' -fuzztime=5s -parallel=2
git diff --exit-code -- go.mod go.sum
```

The local three fuzz runs completed 84,338 / 83,766 / 50,780 executions respectively
without failures. This is bounded smoke fuzzing, not an exhaustive input proof.

## Remaining boundaries

Coverage is deliberately not described as 100% or a proof of correctness.
Uncovered paths include parts of CLI/startup orchestration, some journal teardown
and I/O faults, and vendor/backend behavior. Sarama's mock broker validates wire
requests, not a real Kafka cluster's rebalance, replication or failover. Its
synchronous producer API is not made instantly cancellable during a broker call.
The syslog adapter is tested against real local datagrams and controlled lifecycle
failures; this does not qualify every TCP/TLS behavior of its old dependency.

Existing deliberate discard/bypass behavior is retained. Context cancellation is
not a guarantee to drain all application queues. These tests do not prove
exactly-once delivery, downstream durable ACKs or physical power-loss behavior.
No performance improvement is claimed without a benchmark. See also
[the durability boundaries](reliability.md).
