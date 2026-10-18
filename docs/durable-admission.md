# Durable admission across receivers

Enabling `settings.disk_budget` enables one retained admission contract across
all registered input plugins. Omitting it preserves legacy behavior. This is
local crash durability on a functioning persistent filesystem, followed by
at-least-once export. It is not protection against destruction of the disk or
an exactly-once contract with the remote storage service.

## One state machine

1. Bound and validate the request/frame before publishing it.
2. Transfer message ownership to the existing bounded sync pipeline. A memory
   handoff does not constitute acceptance.
3. Reserve original-record space and future delivery/recovery obligations from
   the one cross-process overall budget, then append through the native journal.
4. Complete the admission receipt only after successful storage synchronization.
5. Translate the receipt to the input protocol. Export proceeds independently;
   the journal owns recovery after acceptance.

`BaseRecv.BeginDurable`, `WaitDurable`, and `SubmitDurable` share receipt and
ownership handling. The journal rejects missing receipts and skip-dump traffic
in budgeted mode, even with free disk space. There is no new receiver WAL or
second storage-accounting implementation. OTLP retains its existing opaque
envelope journal and uses the same overall reservation authority.

Cancellation after publication is an uncertain outcome. The pipeline still
owns the message, and its buffered receipt can finish after the caller leaves.
Append/Sync failures can leave a persisted prefix; a failed response never
promises atomic rollback. Retrying an entire batch can duplicate that prefix.
Use stable application event IDs when downstream deduplication is needed.

## Wire behavior under the overall budget

| Input | Positive outcome | Full/uncertain storage | Sender requirement |
| --- | --- | --- | --- |
| OTLP/HTTP logs, metrics, traces | 200 after the complete envelope is durable | 503 with `Retry-After: 1`; no retryable `partial_success` | Retain and retry the complete request |
| Legacy signed HTTP | 200 with `msgid` after its receipt | 503 with `Retry-After: 1` | Explicit retry policy for POST; timeout may duplicate |
| NDJSON / CloudEvents HTTP | 204 after every record receipt | 503 with `Retry-After: 1`; some earlier records may be durable | Retry the batch and tolerate duplicates |
| Fluent Message / Forward / PackedForward | MessagePack `{"ack": originalChunk}` after every record receipt | Close/withhold ACK; no invented NACK | Enable acknowledged Forward and durable sender buffering |
| Kafka | Mark/commit only after each consumed-order durable frontier | Hold and retry the current failed record; never mark a later record across the gap | Keep broker retention long enough for the outage |
| Syslog TCP / UDP | No durable application ACK exists | Explicit best-effort mode still waits for journal admission; refused new input is counted/logged, without a sender durability promise | Migrate to an acknowledged input for strict delivery |

There is no OTLP/gRPC listener in this release. HTTP error codes must not be
described as gRPC statuses. Ordinary TCP ACKs acknowledge transport bytes only.

Fluent strict mode requires a nonempty chunk (at most 512 bytes). It validates
the whole bounded frame, timestamps, and packed entries before publication;
gzip packed expansion is bounded by `max_frame_bytes`. A partially persisted
batch receives no chunk ACK. Chunkless Fluent requires `best_effort: true`.
Configure a sender's ACK option explicitly: Fluent Bit's
`Require_ack_response` defaults to false.

Kafka retains the current broker record while retrying persistence with
cancelable backoff; it does not consume another record from that claim until
success. Sarama's bounded claim channel may prefetch, while group heartbeats
continue independently. Session cancellation fences marking after a late
receipt. A malformed or oversized strict record returns the claim unmarked;
the pinned Sarama 1.61.1 cancels the session on claim exit, so rejoin retries
from the committed position. A poison record therefore blocks its partition
until operators repair input or explicitly authorize a loss policy. Strict
input is limited to 8 MiB total value/key/topic/header bytes and 4096 headers.
New consumer groups retain the existing `OffsetNewest` policy; configure broker
offsets before startup if historical topic ingestion is required.

## Compatibility and preservation

With a budget, legacy HTTP is durable by default; explicitly configuring
`require_durable_ack: false` is rejected. Syslog TCP and UDP are both enabled by
the syslog plugin, so that plugin must explicitly select `best_effort: true`.
This flag only waives the transport ACK guarantee. It never disables the
shared cap, original retention, or persistence barrier.

Startup rejects prejournal Spring/Spark filters, nonempty `default.add`, Fluent
concatenation, and Fluent tag-key mutation. They can change, re-enter or discard
records before the original is saved. Move such transformations to a deliberately
qualified postjournal stage; the collector does not silently relocate them.
Pure tag validation and throttling can refuse input before persistence and
must complete an error receipt. OTLP-only deployments do not traverse these
legacy filters. Dry mode cannot advertise durable budgeted admission.

The native journal wrapper preserves source context separately from payloads:
Fluent timestamp/options, original legacy HTTP body, Kafka topic/partition/
offset/time/key/value/headers, and original parsed syslog parts. Syslog preserves
the parser output, not a byte-exact datagram. MessagePack timestamps preserve
the time instant, not Go's location pointer. This metadata is omitted from
normal JSON/MessagePack exports and charged as part of the original record.

Remote HTTP success/searchability is not proof of remote disk commit. Until a
stronger durable retirement mechanism exists, acknowledged originals remain
charged locally. A finite retained archive eventually fills and refuses NEW
input. Operators must arrange approved durable off-host retention/retirement;
normal operation never deletes accepted originals to make room. See
[the overall budget and recovery runbook](overall-disk-budget.md).

## Qualification and operational acceptance

Require all of the following before rollout:

- At free capacity, every reliable adapter waits for the actual journal receipt;
  a blocked/failed Sync cannot produce HTTP success, chunk ACK, or Kafka mark.
- All participating roots share one actual coordinator mount/identity and the
  configured total, including funded progress, receipts, quarantine and metadata.
  For b1 the deployment limit is decimal **3,000,000,000 bytes**, not per worker.
- Concurrent full-budget requests refuse new persistence, keep charged plus
  reserved bytes within the limit, and recover when verified capacity returns.
  Existing accepted work can still write its funded completion evidence.
- Isolated crash/restart after ACK recovers the original; cancellation and
  partial batches never masquerade as rollback or exactly-once delivery.
- Kafka tests include failure gaps, consumed-order offsets (numeric gaps are
  valid), cancellation/rebalance fencing, and poison records. Stub session tests
  do not substitute for a real broker rebalance/outage qualification.
- Observe collector admission failures, accounting known/unknown state, pending
  work, producer failures/drops, disk bytes and inodes independently of the log
  sink. A healthy collector counter alone is not end-to-end query acceptance.
- Preserve TLS/auth and validate actual deployed query endpoints/fields with a
  synthetic marker. Source tests do not establish a live deployment or grant
  permission for network/security/credential changes or archive deletion.

The regression `TestOverallDurableIngressRejectsBypass` reproduces both old
bypass paths. Adapter tests exercise receipt timing, chunk validation, batch
prefix uncertainty, Kafka offset gaps, cancellation and actual native metadata
codecs. Real HTTP handlers additionally share an exhausted coordinator and
recover. Existing native journal/OTLP process-crash, retained archive, storage
failure, rotation and sink-recovery tests remain required. Production destructive
fault injection is not part of this qualification.

## Primary protocol references (reviewed 2026-10-18)

- [OTLP retry and partial-success contract](https://opentelemetry.io/docs/specs/otlp/)
- [Forward protocol response](https://github.com/fluent/fluentd/wiki/Forward-Protocol-Specification-v1.5#response)
- [Fluent Bit Forward ACK configuration](https://docs.fluentbit.io/manual/data-pipeline/outputs/forward.md)
- [Kafka consumer position, commits and liveness](https://kafka.apache.org/41/javadoc/org/apache/kafka/clients/consumer/KafkaConsumer.html)
- [Kafka rebalance ownership](https://kafka.apache.org/41/javadoc/org/apache/kafka/clients/consumer/ConsumerRebalanceListener.html)
- [rsyslog reliability limitations](https://docs.rsyslog.com/doc/whitepapers/reliable_logging.html)
- [UDP syslog reliability](https://www.rfc-editor.org/rfc/rfc5426.html#section-4)
- [HTTP retry semantics](https://www.rfc-editor.org/rfc/rfc9110.html#section-9.2.2)
- [OpenTelemetry Collector resiliency](https://opentelemetry.io/docs/collector/resiliency/)

Switching to a standard collector is a valid future consolidation option, but
does not itself prove this deployment's global quota or durable ACK boundary.
For example [file_storage max_size](https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/main/extension/storage/filestorage/README.md)
is per database, and source acknowledgement support remains protocol-specific.
The smallest current repair reuses the existing qualified storage boundary and
adds thin protocol adapters rather than another wrapper or queue.
