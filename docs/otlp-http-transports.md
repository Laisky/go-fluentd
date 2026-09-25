# OTLP/HTTP transport components

## Status

`internal/otlphttp` implements the HTTP receiver handler and exporter for logs,
metrics and traces. They are tested with live HTTP peers, the real journal,
`OTLPProducer` and the durable disposition store. **The application does not yet
register these components through YAML or start an OTLP listener.** This is not a
ready-to-deploy Collector replacement. See the [implementation plan](otlp.md).

## Receiver contract

`NewReceiver(ReceiverConfig, Admission)` returns an `http.Handler` for the exact
POST paths `/v1/logs`, `/v1/metrics` and `/v1/traces`. It accepts protobuf and OTLP
JSON with identity or gzip encoding. It validates the bounded complete request
before invoking `Admission`, including empty and future-field-only envelopes.

The synchronous admission callback must freeze the destination plan, assign a
stable journal namespace/record ID, then write and synchronize the record before
returning nil. Only then can the handler return HTTP 200 with the matching empty
Export response. The callback is not merely queue admission. The integration
tests use actual `WriteData` and `Sync`, but production lifecycle wiring remains
pending. Admission failure or cancellation returns 503, not successful delivery.
An error after writing may be an unknown outcome; it does not promise rollback.

Malformed input is 400, oversized input 413, unsupported or ambiguous
representation 415, failed configured bearer authentication 401, and exhausted
admission capacity 503 with `Retry-After: 1`. Errors use a minimal
`google.rpc.Status`, not HTML or an Export-success response. Internal storage
error text is not returned. Optional bearer authentication must be enabled or
provided by a trusted front end; an empty token does not authenticate requests.

| Receiver setting | Default | Meaning |
|---|---|---|
| `Limits.WireBytes` | 4 MiB | Encoded body bytes, independent of Content-Length |
| `Limits.DecodedBytes` | 4 MiB | Decompressed body bytes |
| `Limits.Items` | 10,000 | Known logs/spans/metric data points |
| `MaxConcurrent` | 16 | Active body decoding plus admission callbacks |
| `Timeout` | 30 seconds | Request body/admission context budget |
| `BodyReadTimeout` | 10 seconds | Body read deadline on supporting servers |

Mount the handler on a dedicated server with TLS, header-size limits,
`ReadHeaderTimeout`, idle timeouts and deployment-specific connection limits.
Admission callbacks must honor their context. ResponseWriter wrappers must expose
`Unwrap` for the response controller or enforce equivalent body deadlines. The
handler sets and clears its body deadline; align any outer server deadline policy.
The active-request limit is not an idle-connection, decoder-allocation, total
process-memory or persistent-backlog quota.

## Exporter contract

`NewExporter(ExporterConfig)` takes exact per-signal HTTP(S) endpoints, including
the signal path or a custom path. URL credentials, query strings and fragments
are rejected. Configure the optional bearer token separately. TLS verifies peer
certificates, supports custom cloned root CAs, and requires TLS 1.2 or newer.
Redirects are not followed. No global HTTP transport is modified.

`Exporter.Send` revalidates the envelope/count, preserves the original payload
encoding, and optionally gzips it once. Retries in the same call use identical
encoded bytes. Bind this callback through `OTLPProducer`/`Store.DoDelivery`; a nil
error by itself is **not** an acknowledgement. The classified outcome determines
whether the destination was accepted, retryable or durably quarantined.

A complete valid HTTP 200 Export response is acceptance. A nonzero partial
rejection is terminal, never full delivery and never automatically retried.
HTTP 429/502/503/504 and transport failures without a received response are the
retry cases. Other statuses, including 204/408/500, are permanent. Malformed,
truncated, oversized or ambiguous response representations are invalid terminal
outcomes even when the received status would otherwise be retryable.

Defaults are three attempts, 100 ms initial exponential backoff, 5 seconds
maximum base backoff, up to 20% added jitter, and a 30-second total call context
budget including retries/waits. The response limit defaults to 1 MiB, applied to
both raw and decompressed bodies; header bytes are separately bounded. Encoded
and decompressed request defaults match the receiver's 4 MiB limits.

`Retry-After` is respected without shortening the server delay. Its per-signal
not-before time also delays subsequent calls on the same exporter instance.
**This state is not durable across restart.** The future journal scheduler must
avoid tight loops after exhaustion and define restart behavior for retry timing.
Already in-flight concurrent requests are not retroactively canceled by a later
throttling response. Cancel active calls before closing their owning pipeline;
`Exporter.Close` only closes idle connections.

## Evidence and ownership

Bounded raw response bytes are retained, including compressed bytes when the
peer used gzip. The diagnostic includes the response Content-Encoding; the
existing receipt schema's content-type field describes Content-Type only.
`Truncated` marks a retained prefix after raw overflow or incomplete reading.
Neither request authorization nor arbitrary response headers are copied into
receipts, but a peer can echo sensitive content into its body: treat all retained
payloads/diagnostics as sensitive and provision private storage.

Known accepted and terminal destination receipts suppress re-export after
reopening. Quarantine remains distinct from full delivery. Receipt persistence
and WAL release rules are in [destination accounting](otlp-accounting.md) and
[disposition recovery](otlp-dispositions.md). These transports do not add receipt
compaction, a disk quota, metrics aggregation, sampling or encoding conversion.
They add parsing, copying and optional compression costs; no throughput or
performance-neutrality claim is made.

## Tests and remaining acceptance

The public transport suite checks exact wire bytes across three signals, two
encodings and gzip/plain bodies; independent generated Status decoding; response
classification; oversized/decompression-corrupt/truncated responses; chunked
input; bearer authentication; body deadlines; cancellation; and TLS/redirects.
Five disposable unsafe-source variants must fail named assertions while an
independent invalid-input/no-network control still passes.

The integration matrix has 12 cases (three signals, two encodings, two journal
codecs). Live HTTP admission writes and synchronizes a real record. Destination
A accepts, B is retryable, and C partially rejects. After closing/reopening the
same journal and receipts, only B is contacted. Total calls are A=1/B=2/C=1;
quarantine does not become full delivery. A second reopen checks the completed
WAL acknowledgement. Unknown request fields and exact bytes survive throughout.

These are component/library integration tests, not new SIGKILL tests, a configured
CLI execution, an external Collector or physical-power-loss qualification. The
previous producer/store subprocess tests remain separate regression protection.
Next implement persistent namespace/ID allocation, dedicated journal lifecycle,
replay scheduling, server shutdown and YAML registration, then run the actual
binary against an independent Collector before advertising OTLP endpoints.

```sh
go test -mod=readonly -count=1 -timeout=180s ./internal/otlphttp
go test -mod=readonly -race -count=3 -shuffle=on -timeout=180s ./internal/otlphttp
python3 .scripts/verify_otlp_http_contracts.py --artifacts /tmp/otlp-http-negative
```

Protocol reference: [OTLP specification](https://opentelemetry.io/docs/specs/otlp/).
