# OTLP support: protocol foundation and implementation plan

## Current state

**Not yet an enabled receiver or sender.** `library/otlpwire` is the tested first
increment for OTLP/HTTP logs, metrics and traces. It does not register routes,
open a listening port, export data, modify the journal, or change legacy defaults.
The application still cannot be configured with `type: otlp`.

This work is separate from CloudEvents/NDJSON PR #16. OTLP is not generic JSON,
and its response/retry contract cannot use the generic HTTP event sender.

| Increment | State | Acceptance needed |
|---|---|---|
| Signal-aware protobuf/JSON request handling, gzip bounds, response decisions | Implemented in `library/otlpwire` | Public-package fixtures, wire exchanges, race tests and fuzzing |
| Durable per-destination handling of partial/permanent rejection | Next | Receipt survives restart; does not become a delivery-success counter or automatic replay |
| OTLP/HTTP receiver and exporter, controller/YAML wiring | Pending | Three endpoints, auth, overload/backpressure, journal admission and error bodies |
| Actual binary plus independent Collector interoperability | Pending | Crash/replay, receiver/exporter wire validation, source/sink reconciliation and negative controls |
| OTLP/gRPC | Not implemented | Separate transport, response/trailer, cancellation and interoperability tests |

Do not mark the overall feature ready or advertise endpoint support until all
HTTP integration gates pass. No profiles, aggregation, sampling, temporality
conversion, Prometheus remote write or Collector-replacement claim is made.

## Intended use

```text
OTel SDK / Collector
       | OTLP/HTTP (protobuf or JSON, optionally gzip)
       v
signal-aware recv -> immutable export envelope -> journal -> OTLP sender
                                                          |
                                                          v
                                           Collector / OTLP backend
```

The standard signal paths are `/v1/logs`, `/v1/metrics`, and `/v1/traces`.
Transport, receiver and exporter configuration are still to be implemented;
this diagram is a target data flow, not a deployment example.

## Why preserve an export envelope

An OTLP Export request becomes one local record with separate signal and content
type metadata. Persist its original **uncompressed bytes**, not a generic JSON
map and not a re-marshaled projection. `Request.Payload()` returns a copy.
The present parser reads the request with the OpenTelemetry Collector's pdata
codecs to check decodability/count known items, then retains the original bytes.
Unknown protobuf fields and unknown OTLP JSON fields remain in those bytes.
Forward using the same content type; there is no cross-encoding conversion here.

This prevents an implicit log filter or `msgid` injection from changing telemetry.
It preserves resource/scope attributes and schema URLs; trace/span IDs, events and
links; log bodies/severity/trace correlation; metric integer/double types,
monotonicity, delta/cumulative temporality, buckets, exemplars and summaries.

OTLP JSON has its own protobuf mapping: trace/span IDs are hexadecimal, integer
enums are required on the wire, and 64-bit integers have a string representation.
Collector codecs, not generic JSON-to-`float64` maps, decode these types. No
arbitrary-precision computation is performed by this forwarding foundation.

The item count is spans, log records or metric **data points**. Five metric
descriptors can contain six or more points. Limits count points correctly.
A nonempty envelope with zero *known* items must not be thrown away; it can carry
unknown future schema fields. The module is a bounded decoder, not a complete
validator of every semantic convention. Protobuf signal selection comes from the
endpoint, not format autodetection: different signals can share wire field numbers.

## Response handling is part of the data-loss contract

| OTLP result | Foundation outcome | Future integration obligation |
|---|---|---|
| HTTP 200 and valid empty Export response | `Accepted` | Finish this destination's delivery |
| HTTP 200, zero rejected items, diagnostic warning | `Accepted`, with diagnostic | Record warning; do not retry |
| HTTP 200 with nonzero rejected items | `PartiallyRejected` | Never claim full delivery or automatically retry this request |
| HTTP 429, 502, 503 or 504 | `Retryable` | Bounded exponential backoff/jitter; respect Retry-After |
| Other received HTTP status, including 204, 408 and 500 | `PermanentlyRejected` | No protocol-level retry; persist a terminal disposition |
| Malformed, oversized or content-type-mismatched response | `InvalidResponse` plus error | Not success; do not silently ACK or auto-retry |

A full success response is HTTP **200**, not the CloudEvents receiver's 204. JSON
returns `{}`; protobuf returns a zero-length serialized Export response. Its
Content-Type must match the request. The caller must only emit it after the actual
receiver acceptance contract is met. `SuccessBody` does not wait for a WAL Sync.
Network failure before obtaining a response is a separate retry decision.

Partial success is especially important for metrics. The backend reports how
many points were rejected, not which ones. Retrying the entire delta batch can
count already-accepted points twice. OTLP explicitly prohibits retrying a request
whose partial_success is populated. No retry is attempted by this package.

**The current generic producer has no durable terminal-rejection result.** Before
wiring an OTLP sender, add an explicit per-destination rejection path. The proposed
policy retains the complete original envelope and peer response durably, records
rejected counts separately from delivered counts, and prevents journal replay
from resending an already known terminal rejection. Never turn a `partial_success`
into ordinary sender success or ordinary replayable failure. Test a restart after
the terminal disposition was persisted, and a failed persistence attempt.

Persisting rejected data is a quarantine/diagnostic guarantee, not a delivery
guarantee. The exact rejected subset is unknowable from just a count. Network/crash
ambiguity before a terminal response is durably recorded can still cause duplicate
exports; no exactly-once or automatic delta-deduplication guarantee is proposed.

## Bounds and ownership

`DefaultLimits()` returns 4 MiB for compressed wire bytes, independently 4 MiB for
decompressed payload, and 10,000 items. Helpers require positive limits and
reserve room for one extra byte to detect overflow. They do not rely on an HTTP
Content-Length. Gzip checksum errors and truncated streams are errors, not EOF
success. Identity and gzip are supported; stacked/unknown encodings are rejected.
Response bounds apply even to otherwise retryable status codes. An oversized
response is not retried, as required by OTLP 1.11.0.

Limits are per request/response, not global process memory, connection, decoder
allocation or disk-backlog quotas. The eventual server must add body/header
read deadlines, admission limits, authentication and TLS. Callers close body
readers; these pure helpers neither close connections nor launch goroutines.
`RetryDelay` parses delay seconds and HTTP dates without sleeping, adding jitter,
or prematurely capping a legitimate server-requested delay.

## Tests and reproduction

The test fixtures under `library/otlpwire/testdata` are literal OTLP JSON, not
produced by this module. Binary request fixtures use Collector encoding and an
independent extra unknown protobuf field. Response fixtures include manually
encoded protobuf bytes for partial success and signed-count errors.

Tests assert complete byte preservation and decoded semantic values for all three
signals in JSON/protobuf and plain/gzip. Metrics include gauge, sum, histogram,
exponential histogram and summary, a NaN gauge, MinInt64/MaxInt64, delta and
cumulative data, and exemplars. Logs include MaxUint64 timestamps and binary
bodies. Trace end/start timestamps differ by exactly one nanosecond.

Additional tests cover status classification, warning-only/partial rejection,
invalid counts, response limits, gzip checksum, chunked HTTP with unknown length,
read errors and Retry-After dates/overflow. The HTTP tests are real local wire
exchanges, **not full go-fluentd recv -> journal -> sender or external Collector
interoperability tests**. Those remain explicit integration gates above.

```sh
go mod verify
go build -mod=readonly ./...
go vet -mod=readonly ./...
go test -mod=readonly -count=1 -timeout=180s ./...
go test -mod=readonly -race -count=10 -shuffle=on ./library/otlpwire
go test -mod=readonly -run '^$' -fuzz '^FuzzOTLPWire$' -fuzztime=5s -parallel=2 ./library/otlpwire
go test -mod=readonly -run '^$' -bench '^BenchmarkOTLPRequest$' -benchmem -count=3 ./library/otlpwire
```

Benchmarks measure parsing/retaining one fixture envelope, not durable throughput.
No performance improvement or production capacity is claimed. The Collector
pdata dependency also updates selected transitive modules. Use the committed
module graph and run the entire legacy suite; do not treat this as a dependency-
free change or copy tests onto an incompatible old dependency graph.

## References

- [OTLP 1.11.0 specification](https://opentelemetry.io/docs/specs/otlp/)
- [Collector pdata](https://github.com/open-telemetry/opentelemetry-collector/tree/pdata/v1.66.0/pdata)
- [Metrics data model](https://opentelemetry.io/docs/specs/otel/metrics/data-model/)
- [Logs data model](https://opentelemetry.io/docs/specs/otel/logs/data-model/)

Snapshot: 2026-09-24. This work does not change the already-reviewed CloudEvents
PR, the original README architecture diagram or the existing journal format.
