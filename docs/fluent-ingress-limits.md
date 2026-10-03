# Fluent TCP ingress resource limits

The Fluent listener validates MessagePack framing before invoking the generated
codec. Do not call `FluentBatchMsg.DecodeMsg` directly on an untrusted socket or
packed payload: the generated decoder allocates from wire counts. Regenerating
`library/data_gen.go` does not remove the ingress guard.

## Configuration

The following keys belong to a `type: fluentd` entry under
`settings.acceptor.recvs.plugins.<name>`. These are policy defaults, not measured
capacity recommendations for a particular host.

| Key | Default | Meaning |
| --- | --- | --- |
| `max_connections` | 32 | Concurrent admitted TCP workers per listener, shared across rebinds. |
| `idle_timeout` | `30s` | Time to wait for the next frame's first byte. |
| `frame_timeout` | `10s` | Absolute read deadline after the first byte; additional bytes do not extend it. |
| `max_frame_bytes` | 8388608 | Encoded bytes in a single frame, including headers. |
| `max_value_bytes` | Same as `max_frame_bytes` | Bytes in one string, binary value or extension. |
| `max_container_elements` | 4096 | Members of a nested array or key/value pairs of a map. |
| `max_values` | 65536 | Total values, including containers and map keys, shared by the outer frame and all packed entries. |
| `max_depth` | 32 | Nesting depth, with the frame root at depth 1; hard ceiling 128. |

Durations require units. Omitted/zero settings select defaults; negatives are
configuration errors and never disable enforcement. `max_value_bytes` cannot
exceed `max_frame_bytes`. Tune the connection and frame limits together: even
32 connections times 8 MiB represents 256 MiB of encoded frames, before decoded
objects, buffer growth, maps, downstream queues, and other process memory.
These limits are **not** a global process RSS quota or a downstream queue-byte
budget. Smaller hosts should use lower connection/frame limits and retain
process/container memory limits and network access restrictions.

Excess accepted sockets are closed before launching workers. Rejected sockets
are not logged individually by the admission loop, avoiding an overload-log
amplification path. All worker exit paths release their connection slot.

## Protocol and delivery behavior

Message, Forward and binary PackedForward forms continue to use the existing
MessagePack codec and receiver routing. The outer array is bounded to 2–4
fields (including optional options); packed entries must have exactly two
fields. A rule requiring exactly three outer fields would break Forward and
PackedForward traffic. Existing handling of malformed Forward records is
unchanged. This change does not implement new compression, authentication or
acknowledgement capabilities.

All nested array/map counts, string/bin/extension lengths, aggregate value
counts, encoded frame bytes and depth are checked before general-purpose
object allocation. The framing buffer grows for bytes actually received, not
for a declared-but-missing payload. It preserves pipelined bytes across frame
boundaries. Packed entries share the outer frame's value budget; the budget
resets only for a new outer frame, not for each packed entry.

Malformed or oversized input closes the connection; parsing does not attempt
to recover from an unknown stream position. Valid packed entries already
published before a later malformed packed entry are not rolled back. A TCP
write succeeding is not a durable application acknowledgement. Reconnect/retry
behavior and duplicate handling remain the sender's responsibility.

The timeouts bound stalled **reads**, not time spent waiting for downstream
capacity. Downstream backpressure retains an admitted, bounded worker and
remains cancelable by receiver shutdown; a read timeout does not silently drop
an already-decoded record waiting for downstream admission. Completed frame
references are cleared before the next idle wait.

## Regression validation

`TestFluentIngressRejectsOuterHeaderBeforeBody` is deliberately compatible with
the old receiver and uses a moderate 65,536-member declaration. It fails on the
old path because the decoder allocates and waits for the missing body. It does
not send a maximal array declaration into the vulnerable production decoder.

The fixed-path suite additionally covers maximal outer/nested/packed headers,
large string/bin/extension declarations, exact byte boundaries, aggregate
values, nesting, truncations, fragmented/pipelined frames, existing wire forms,
record ownership, idle/stalled/trickled reads, overload rejection, slot recovery
and cancellation. The scanner includes an allocation-budget regression, a fuzz
target and representative-frame benchmarks.

```sh
go test -mod=readonly -race -count=10 -shuffle=on ./internal/recvs/...
go test -mod=readonly -race -count=1 ./...
GOMAXPROCS=2 go test ./internal/recvs/fluentwire -run '^$' -fuzz '^FuzzReadFrame$' -fuzztime=30s
go test ./internal/recvs/fluentwire -run '^$' -bench . -benchmem
```

The repository's pinned Go toolchain/dependencies must be used for production
receiver tests. A scanner-only test run using the standard library does not
substitute for the full receiver and repository tests.
