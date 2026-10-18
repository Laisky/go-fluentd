# Faithful records in a flat log store

Status: source candidate; isolated typed-storage campaign passed. No live adoption.

VictoriaLogs' native OTLP mapping flattens maps into field names and converts
values to strings. Nested paths can collide with literal dotted keys, resource
and record attributes can collide, null/empty maps disappear, and byte/numeric
types cannot be reconstructed from those query strings. Version 1.52 can also
panic on duplicate stream fields; the upstream 1.53 release fixes that panic.
An array-based JSON workaround is unsuitable: native nonfinite values produce
`+Inf`/`-Inf`, and bytes and numeric type distinctions still collapse.

The explicit destination option `log_storage_codec: otlp-record-base64-v1`
adapts log storage while retaining the application's data. It uses the existing
OTLP HTTP transport, authentication, fsynced WAL, persistent gateway queue and
retry behavior. The default remains transparent OTLP forwarding. Metrics and
traces retain their existing representation.

Each original log record becomes one stored row:

- `_msg` contains the original string body. A nonstring body is displayed as its
  standard typed OTLP JSON AnyValue. An empty string may have no native `_msg`
  field; its exact original body remains in the archive.
- `otel.record.otlp` contains base64 of a one-record OTLP export request with the
  original resource, scope and record context. For protobuf, original record
  bytes and non-child context fields are copied without re-marshaling, including
  unknown protocol fields. JSON field values retain their raw representation.
- `otel.record.content_type` identifies the archived representation.
- `otel.record.json` contains a standard typed OTLP JSON view for the pinned known
  schema. It retains application keys, nested values, nulls, empty structures,
  bytes, nonfinite doubles and numeric type tags. The raw archive is authoritative
  for unknown future protocol fields.
- `otel.storage.format` identifies this codec. Only `service.name`, `host.name`
  and `deployment.environment.name` are copied into native stream indexes. All
  original resource attributes remain in the archive and typed JSON view.

No content allowlist, key denial, secret-pattern replacement, scalar-only filter,
body substitution policy or silent truncation is applied. Application-owned
content and level choices remain with the application. This option changes the
storage representation; direct native field queries must use the typed view.

## Configuration and limits

Add the option to the existing named destination, preserving its endpoints,
credential references and TLS settings:

```yaml
settings:
  otlp:
    destinations:
      - id: existing-private-home-gateway
        # Keep the existing logs/metrics/traces endpoints and TLS/auth settings.
        log_storage_codec: otlp-record-base64-v1
```

The snippet is not a complete service configuration. An unknown codec fails
configuration validation. No network or credential change is needed for the
encoding itself; normal release and operator approval gates still apply.

Archive and JSON-view expansion consumes space and CPU. The existing configured
decoded and wire budgets remain in force. Before admitting new work to the WAL,
the collector runs the same destination encoder/compressor and budget checks
used by replay transport. Oversized output receives HTTP 413, increments
`otlpStorage.codecAdmissionRejected`, and creates no durable identity or pending
record. No partial record is accepted. The codec also checks a conservative upper bound
on VictoriaLogs' hard 2 MiB estimated JSON row size, with a 1 KiB reserve
for native generated/default fields; that sink otherwise ignores
oversized rows while returning HTTP200. Original long field names and many keys
remain inside the typed archive/view, avoiding its 128-byte native field-name
and 2000-native-field limits. No source field is shortened or removed. Malformed unsupported representation
failures also reject the whole request and increment that counter.

An already accepted historical record that cannot fit a newly selected encoding
stays pending; no success, quarantine or silent deletion occurs. Stop new
admissions if necessary, preserve the WAL and receipts, and qualify a sufficient
budget or restore the previous encoding configuration. Never delete retained
work to clear this condition. This is an attended recovery operation.

Input structure/item/depth limits still apply. Expansion checks are allocation
bounds, not a precise process heap quota; retain container memory/CPU/PID limits.
Recordless resource/scope containers do not represent log records and have no
stored row. Zero-record requests retain the existing transparent forwarding
contract; the per-record codec does not change empty-batch behavior. Ambiguous duplicate JSON envelope keys are rejected. Duplicate
application keys within attribute arrays remain in the raw archive.

The application SDK queue is volatile until collector admission. Delivery is
at least once under ambiguous acknowledgements; controlled replay tests cannot
establish universal exactly-once delivery. Retention and disk capacity remain
separate operational limits.

## Query and presentation

The synthetic campaign verified these expressions against pinned VictoriaLogs
v1.52.0, with the fixture service name substituted for the application name.
Use the operator-verified query endpoint; this option exposes no new listener:

```text
service.name:="graphql" otel.record.json:~"chosen value"

service.name:="graphql"
  | unpack_json from otel.record.json preserve_keys (resourceLogs) result_prefix "original."
  | fields _time, _msg, original.resourceLogs
```

Preserving the `resourceLogs` array during query avoids recreating flattening
collisions. Use the typed `attributes` arrays and AnyValue tags to distinguish
resource versus record keys, bytes versus text, and integers versus doubles.
Decode the raw base64 archive with the stated content type when unknown protocol
fields or exact original wire values matter. Existing historical rows retain
their previous schema; select by `otel.storage.format` when mixing formats.

## Qualification required

Require typed protobuf/JSON round trips; unknown context fields; body and scope
collisions; null/empty structures; large and unfamiliar fields; observable
pre-admission expansion refusal; unchanged transparent other signals; same-WAL
retry/restart; sink outage and fsynced gateway crash/replay; actual stored archive
equality; meaningful content queries; and no production data or credentials.
The local source campaign passes typed protobuf/JSON round trips, actual listener
HTTP413/no-WAL admission refusal, unequal wire/decoded budgets, gzip transport,
retry/restart, and complete raw archive equality through the real pinned gateway
and VictoriaLogs. The sink-outage campaign also passes fsynced gateway SIGKILL
recovery and the queries above. Exact receipts and source/image provenance belong
in the release review. Keep the source Draft pending parent review and hosted
checks; passing source tests does not authorize deployment.

Primary references:

- [OpenTelemetry attribute type mapping](https://opentelemetry.io/docs/specs/otel/common/attribute-type-mapping/)
- [VictoriaLogs 1.53 release](https://github.com/VictoriaMetrics/VictoriaLogs/releases/tag/v1.53.0)
- [VictoriaLogs LogsQL JSON presentation](https://docs.victoriametrics.com/victorialogs/logsql/#unpack_json-pipe)
