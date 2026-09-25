# HTTP event formats

Go-Fluentd can receive and send **CloudEvents 1.0 over HTTP** and **NDJSON** using
`type: http_events`. These are separate from the legacy signed-log `http` receiver
and from Elasticsearch bulk. They use the existing journal and routing pipeline.

## Supported wire profiles

| Format | Receiver | Sender | Content-Type |
| --- | --- | --- | --- |
| CloudEvents structured JSON | Yes | `mode: structured` (default) | `application/cloudevents+json` |
| CloudEvents binary HTTP | Yes | `mode: binary` | Payload media type; context in `ce-*` headers |
| CloudEvents JSON batch | Yes | `mode: batch`, explicitly enabled | `application/cloudevents-batch+json` |
| NDJSON objects | Yes | No `mode` option | `application/x-ndjson` |

One CloudEvents receiver accepts all three listed encodings. It can forward them
to any listed CloudEvents sender mode. Structured/binary output sends one event
per request; batch output requires a recipient that supports that mode.

The NDJSON profile requires one non-null JSON object per line, a final LF, and
UTF-8. CRLF is accepted and empty lines are ignored. Arrays/scalars at record
level, duplicate object keys, malformed Unicode, non-finite/out-of-range numbers,
and nesting beyond 64 containers are rejected. Integer literals preserve the
signed/unsigned 64-bit range; fractional/exponent values use finite IEEE-754
float64, not arbitrary-precision decimal arithmetic.

CloudEvents validates `specversion: "1.0"`, `id`, `source`, `type`, optional
standard attributes and the JSON envelope. `data` and `data_base64` are mutually
exclusive. Binary opaque bytes are represented internally with `data_base64`.
Do not infer types of custom extensions from HTTP headers: binary extension
values are strings unless their extension definition supplies a type. Converting
structured typed extensions to binary therefore does not promise JSON type
identity for unknown extensions. Core attributes and JSON event data are retained.
Only absent/UTF-8 MIME charsets are accepted by this implementation.

Request `Content-Encoding: gzip` is **not** supported; unsupported encodings are
rejected rather than decoded without a decompression bound. This does not restrict
plain/gzip **journal** storage. These plugins do not implement OTLP, gRPC, NATS,
JetStream, MQTT, Avro/Protobuf schema registries, SSE or WebSocket transports.

## Configuration

Use [http-events.yml](settings/http-events.yml) as an operator template. It has a
CloudEvents receiver at `/events` and a structured sender targeting a placeholder
loopback service. It is not a bundled downstream server. Start it only after
providing a compatible destination:

```sh
mkdir -p build
go build -mod=readonly -o build/go-fluentd .
./build/go-fluentd --config docs/settings/http-events.yml --env prod --addr 127.0.0.1:8080
```

Submit one event (replace the public demo token before deployment):

```sh
curl --fail-with-body -i http://127.0.0.1:8080/events \
  -H 'Authorization: Bearer replace-this-demo-token' \
  -H 'Content-Type: application/cloudevents+json' \
  --data-binary '{"specversion":"1.0","id":"order-42","source":"/orders","type":"example.order.created","data":{"order":42}}'
```

Expect **204 only after local durable acceptance**. A stopped downstream does not
prevent this local acknowledgement if the journal can accept the event. It can
still accumulate a backlog: local acceptance is not final delivery.

For NDJSON, change both plugins' `format` to `ndjson`, remove the sender's `mode`,
and submit `application/x-ndjson` with a terminating newline:

```sh
printf '%s\n' '{"event":"order-42","count":42}' | \
  curl --fail-with-body -i http://127.0.0.1:8080/events \
  -H 'Authorization: Bearer replace-this-demo-token' \
  -H 'Content-Type: application/x-ndjson' --data-binary @-
```

### Receiver options

Under `settings.acceptor.recvs.plugins.<name>`:

| Setting | Default / requirement |
| --- | --- |
| `type` | Required: `http_events` |
| `active_env` | Include the selected CLI environment |
| `format` | Required: `cloudevents` or `ndjson` |
| `path` | Required literal absolute POST route; no Gin parameters/wildcards |
| `tag` | Required fixed routing tag; `{env}` is expanded |
| `bearer_token` | Empty disables plugin authentication |
| `max_body_byte` | 4 MiB per request, including unknown Content-Length requests |
| `max_records` | 1,024 per request |
| `ack_timeout_sec` | 30 seconds for queueing/receipts **after body parsing** |

Zero numeric values select defaults; negative limits are rejected. This receiver
always uses durable receipts and refuses global dry mode. It does not need the
legacy receiver's `require_durable_ack`, signature, timestamp or flattening options.

| Status | Meaning |
| --- | --- |
| `204` | All events in this request received successful local acceptance receipts |
| `400` | Invalid format, envelope, JSON or record limit |
| `401` | Configured Bearer authentication failed |
| `413` | Request exceeds the byte limit |
| `415` | Unsupported media type/charset or content encoding |
| `503` | Pipeline/storage failure, missing wiring, timeout or cancellation |

The entire request is validated **before any event is published**. This is not an
atomic batch transaction: a 503, disconnect or timeout **after publication** may
leave some or all events accepted. Retry can duplicate them. The receiver does
not deduplicate CloudEvents `(source, id)` pairs; downstream consumers must be
idempotent. Empty valid batches produce no records.

### Sender options

Under `settings.producer.plugins.<name>`:

| Setting | Default / requirement |
| --- | --- |
| `type`, `active_env`, `format` | As above |
| `addr` | Required absolute HTTP(S) URL without userinfo or fragment |
| `tags` | Required routing tags; `{env}` is expanded |
| `mode` | CloudEvents: `structured`, `binary`, or `batch`; omit for NDJSON |
| `bearer_token` | Empty disables outbound Bearer authentication |
| `msg_batch_size` | 1 for structured/binary; otherwise 64; maximum 1,024 |
| `max_wait_msec` | 100 ms batching interval; first delivery is immediate |
| `forks` | 1 worker; maximum 128 |
| `max_body_byte` | 4 MiB for the **complete encoded output batch** |
| `max_response_byte` | 64 KiB; larger/incomplete responses are not accepted as ACKs |
| `request_timeout_sec` | 10 seconds per request |
| `max_attempts` | 3 immediate attempts; maximum 10 |
| `retry_backoff_msec` | 200 ms initial exponential retry delay |
| `max_retry_delay_sec` | 30 seconds cap, also applied to Retry-After |

`settings.producer.sender_inchan_size` bounds this sender's queue (1,024 if not
specified). Negative values are invalid; zero selects the respective default.
Structured/binary modes reject batch sizes greater than one. The sender refuses
`is_discard_when_blocked: true` and global dry mode.

A complete, bounded **2xx** response acknowledges the entire output request.
Configure only peers whose contract accepts every submitted event before that
response. **No per-item acknowledgement body is interpreted.** An Elasticsearch
bulk endpoint returning HTTP 200 with item failures needs the `es` sender, not
this plugin. A peer ACK is only as durable as the peer's own documented contract.

Network failures, HTTP 408, 429 and 5xx receive bounded immediate retries.
Retry-After seconds and HTTP dates are honored up to the configured cap; waits
and requests are cancelable. The encoded body is reused unchanged for immediate
retries. Other non-2xx statuses, invalid output records and output size failures
are not acknowledged; they remain dependent on journal replay and operator repair.
Permanent rejection is **not** a dead-letter policy. Correct the data/routing/peer
before backlog growth exhausts storage. Tune batch size so encoded batches fit
both ends' limits; a size failure rejects the whole batch, even if individual
records would fit separately.

Redirects are never followed. HTTPS verifies the peer certificate and uses TLS
1.2 or later. There is no insecure-skip-verification option. Batch success is
reported once per message, input closure flushes an unfinished batch, and
cancellation does not invent successful delivery.

## Identity, storage and operations

Event-origin messages carry local-only format metadata through the journal.
Unlike legacy logs, they are not implicitly flattened, have no injected payload
`msgid`, and bypass the default log filter's dotted-key rewriting, empty-key
removal and length truncation. The producer's original CloudEvents `id` and any
user `msgid` stay unchanged. A single-event request can include
`X-Go-Fluentd-ID`, a local transport identity derived from the WAL ID; it is not a
new CloudEvents ID. Multi-event requests do not use a single identity header.
Explicitly configured additional filters can still transform or discard events;
do not apply legacy log transformation/drop filters to event tags unintentionally.

The optional `source_format` field is stored outside the event in the existing
journal record wrapper. Journal encoding and dependency versions are unchanged.
Do not assume rolling back to an older binary preserves the new envelope policy;
older application versions ignore this metadata. Stop the old writer before
starting another process against the same persistent journal directory.

Route every event tag to its required destinations and monitor failures/backlog.
The application's existing unmatched-tag and explicitly configured lossy-filter
policies have not been redefined by these plugins. One successful destination
cannot acknowledge another failed destination. Group commit remains opt-in:
`settings.journal.group_commit_max_messages: 64`; 0/1 retains per-record Sync.
There is no total-backlog quota or exactly-once guarantee.

The receiver shares the management HTTP listener. Bind it to a trusted network
or loopback and put authentication, TLS termination, connection/request limits,
header/body read timeouts and rate limiting at a trusted reverse proxy. Its
receipt timeout is **not** a slow-upload timeout or a process-wide memory quota.
The plugin token is optional, and the shared management endpoints are not thereby
authenticated. Treat journal files/config tokens as sensitive. Do not log or post
real payloads/tokens when reporting a failure.

## Executable acceptance

```sh
go test -mod=readonly -race -count=10 -shuffle=on -timeout=180s \
  -run 'Test(ComponentHTTPEvent|ComponentEvent|RegressionEvent)' ./...
go build -mod=readonly -race -o /tmp/go-fluentd-events .
python3 tests/event_formats/run.py --binary /tmp/go-fluentd-events \
  --artifacts /tmp/event-evidence --group 64 --seed 991
python3 tests/event_formats/audit.py --artifacts /tmp/event-evidence
```

The permanent workflow repeats four output profiles, mixed CloudEvents inputs,
plain/gzip journals, two group policies and two seeds. It uses actual executable
HTTP requests, independent HTTP sinks that fsync their ledgers, real SIGKILL,
the same journal directory across restarts, peer failures/lost ACKs, and an actual
filesystem refusal. The saved-file auditor regenerates the producer fixtures,
checks raw outgoing bytes against the ledgers and checks complete fields/types,
receipts, identities and summaries. Identical retries are permitted. Five damaged
evidence controls must fail, including jointly changing manifest and wire data.
These are not real broker/collector deployments or physical-power-loss tests.

Specifications: [CloudEvents HTTP binding](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/bindings/http-protocol-binding.md),
[CloudEvents JSON format](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/formats/json-format.md),
and [NDJSON specification](https://github.com/ndjson/ndjson-spec).
