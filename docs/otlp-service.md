# Configured OTLP/HTTP service

The opt-in `settings.otlp` section starts a **separate** OTLP/HTTP listener and
pipeline for logs, metrics and traces. It binds the existing wire parser,
receiver, dedicated journal owner, destination accounting and HTTP exporter.
It never feeds telemetry through the legacy log filters or implicit `msgid`
injection. The existing management listener remains separate.

The configured executable has local-process acceptance tests and a
[checksum-pinned standalone Collector campaign](otlp-collector-acceptance.md).
The [branch consolidation](branch-consolidation.md) also checks event and OTLP
pipelines in one process. These bounded tests do not certify production capacity.
This is not OTLP/gRPC, profiles, aggregation, sampling, temporality conversion
or a replacement for the Collector.

## Start from the tested configuration

Use [the actual YAML template](settings/otlp.yml). From the repository root:

```sh
install -d -m 700 ./var/otlp ./var/legacy-journal
# Supply a strong token through your secret-management system, not this file.
export GO_FLUENTD_OTLP_TOKEN='replace-this-before-deployment'
go run . --config docs/settings/otlp.yml --env sit --addr 127.0.0.1:8080
```

The template points at a development peer on loopback port 14318; configure your
real destinations before expecting delivery. Every destination has an immutable
logical ID and three **exact** signal URLs, including their paths. The admission
plan currently sends every request to all configured destinations. Partial
signal routing is not implemented; missing signal endpoints are rejected at
startup rather than accepted as undeliverable data.

The dedicated listener exposes only POST `/v1/logs`, `/v1/metrics`, `/v1/traces`.
Protobuf and OTLP JSON are forwarded without cross-encoding conversion; identity
and gzip request bodies are supported. A matching HTTP 200 Export response means
local WriteData + Sync completed, **not** downstream delivery. Request failure
or connection loss after writing does not promise rollback.

Absent configuration or `enabled: false` does not start OTLP. Enabled configuration
rejects unknown fields, string-to-boolean coercion and fractional or unsafe numeric
limits. Duration values require unit-bearing strings, such as `30s`. `--dry`
is incompatible with OTLP durable admission. Startup/configuration errors return
nonzero process status. Existing CLI errors now also propagate nonzero status.

## Listener security and bounds

The default address is `127.0.0.1:4318`. Use a numeric IP, not a DNS name. All
listeners require a nonempty token resolved from `bearer_token_env`; an unset or
invalid variable is an error. No token value is saved in YAML-derived journal
plans. Exact authorization matching is enforced by the receiver.

Plain HTTP is permitted only on loopback. Other bind addresses require
`tls_cert_file` and `tls_key_file`. TLS requires version 1.2 or newer. Optional
`client_ca_file` additionally requires and verifies a client certificate; it does
not replace bearer authentication. Destination `ca_file` supplies a custom trust
pool; verification cannot be disabled. Prefer HTTPS for non-loopback exporters.
Request tokens and arbitrary response headers are not copied into receipts, but
telemetry and peer diagnostics can themselves be sensitive. Protect storage.

The listener uses HTTP/1.1 deliberately, without HTTP/2 stream multiplexing. Its
connection limit includes active and idle accepted connections; excess sockets
remain subject to the operating system's listen backlog. It does not promise an
HTTP 503 to clients waiting for an accepted connection. The separate receiver
admission limit returns 503 when body-decoding/admission slots are exhausted.

| Setting | Default |
|---|---:|
| `max_connections` | 128 |
| `max_concurrent` decode/admission calls | 16 |
| `max_header_bytes` | 32 KiB (Go HTTP parsing allows its documented buffer slack) |
| `read_header_timeout` | 5 seconds |
| `body_read_timeout` | 10 seconds |
| `request_timeout` | 30 seconds |
| `idle_timeout` | 30 seconds |
| `shutdown_timeout` | 10 seconds |
| `max_wire_bytes` / `max_decoded_bytes` | 4 MiB each, independently |
| `max_items` | 10,000 log records, spans or metric data points |
| `max_response_bytes` | 1 MiB |
| `max_wal_bytes` | 256 MiB logical WAL admission threshold |
| `replay_batch` / `replay_interval` | 64 envelopes / minimum 1 second |

These are not aggregate memory, decoder-allocation or filesystem quotas. Receipt
files and recovery copies are not included in the WAL admission threshold.
Accepted and quarantined envelopes currently retain per-destination copies with
no compaction or receipt TTL. Capacity requires operational monitoring.

## Startup, failure and stop

The service validates transport configuration and TLS material, then binds its
listener **before** initializing storage. A port conflict must not create a new
journal generation. Failed storage startup closes the reserved listener.

The OTLP root must already exist on private persistent storage. The controller
rejects equal or nested OTLP/legacy journal paths in either direction, including
existing symlink aliases. Neither replay engine may discover the other's wrapper
format. This is a configuration guard on trusted local paths, not protection
against a malicious actor changing symlinks during startup.

A fatal listener or journal error stops the dedicated service and cancels the
application controller. SIGINT/SIGTERM cancels HTTP requests and the scheduler,
then closes storage after active work ends. An HTTP shutdown timeout forces
connection closure. Kernel file Sync is not context-interruptible and can exceed
the HTTP shutdown budget. Shutdown is not a guarantee that every accepted
request has reached every peer; accepted pending work remains on disk.

Saved acceptance or quarantine receipts prevent another peer's retry from
resending already recorded outcomes. Quarantine is not full delivery. The
management `/monitor` response exposes an `otlp` section with process-local
accepted/quarantined/retryable/blocked envelope counters and receipt replay hits.
Counters are observations, not durable lifetime totals or telemetry item counts.
No health, profiling, or management endpoint is mounted on the OTLP listener.

Keep generation metadata, WAL and receipts together on restart. Removing a
required destination blocks its saved plans rather than erasing obligations.
A crash between a remote response and local receipt persistence can duplicate
exports. Retry-After scheduling is process-local. There is no exactly-once or
physical-power-loss qualification.

## Reproduce the acceptance tests

```sh
go test -mod=readonly -count=1 -run '^TestOTLPService' ./internal/controller
go test -mod=readonly -race -count=3 -shuffle=on -run '^TestOTLPService' ./internal/controller
go build -mod=readonly -race -o /tmp/go-fluentd-otlp .
GORACE='halt_on_error=1 exitcode=66' python3 tests/otlp_service/run.py \
  --binary /tmp/go-fluentd-otlp --artifacts /tmp/otlp-service-evidence
python3 .scripts/verify_otlp_service_contracts.py --artifacts /tmp/otlp-service-negative
```

The black-box suite starts the ordinary executable from actual configuration,
not a test-only Go entry point. Twelve cases cover three signals, JSON/protobuf,
plain/gzip WALs, wire gzip, full/retryable/partial peer responses, two SIGKILL
restarts, fresh post-restart admission, and graceful SIGTERM. Peers independently
record synchronized wire/status ledgers. An auditor regenerates the caller's
workload and checks exact payloads, metadata, receipts and destination outcomes.
Known-completion checkpoints use post-Sync counters only for synchronization;
receiving peer bytes alone is not a durable completion receipt. Unknown remote
outcomes can legitimately duplicate data outside these controlled checkpoints.

Source mutants must fail named assertions for cleartext non-loopback exposure,
missing authentication, missing connection bounds and missing header deadlines.
Positive controls must keep passing. Neither compilation errors nor timeouts are
counted as a valid mutation-test result.

See the [wire plan](otlp.md), [transport contract](otlp-http-transports.md),
[journal lifecycle](otlp-journal-lifecycle.md), [accounting](otlp-accounting.md),
and [receipt recovery](otlp-dispositions.md).

Primary references: [OTLP specification](https://opentelemetry.io/docs/specs/otlp/),
[Go HTTP server](https://pkg.go.dev/net/http#Server), and
[OpenTelemetry configuration security](https://opentelemetry.io/docs/security/config-best-practices/).

## JSON configuration compatibility

JSON configuration files can use integer-valued numeric limits, including nested
destination attempt counts. The configuration library represents JSON numbers as
float64; exact integers below 2^53 in absolute value are converted with target-type
range checks. Fractions, non-finite values, numeric strings and values at or beyond
that precision boundary are rejected rather than rounded. Durations still require
unit-bearing strings. This also accepts mathematically integral YAML float scalars;
it does not enable general string/bool/number coercion. Use YAML integer scalars
for limits larger than the exact JSON-number conversion boundary.
