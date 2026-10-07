# Multiline pending-state limits

The configured process shares **one** budget across all Fluent receiver workers
and all post-journal concatenator tags/workers. These are not independent caps
which can be multiplied by `n_fork` or by creating another tag.

```yaml
settings:
  concat:
    max_pending_messages: 1024
    max_pending_bytes: 16777216
```

Zero/omitted selects the finite defaults above. Negative values are invalid.
Positive limits are at most 65,536 messages and 1 GiB of accounting units.
Standalone Go users receive a finite budget per receiver/factory; embedders
creating several pipelines must pass the same `*concatstate.Budget` through
`ConcatBudget` to impose a common process budget.

## Saturation and ownership

A head which cannot reserve an entry and its complete estimated size is emitted
unchanged instead of retained. An existing head whose tail cannot reserve text
and acknowledgement-array growth is emitted first; the tail then travels as a
separate record. Under pressure, multiline grouping is best effort, **not** a
reason to drop records or acknowledge them. Operators must tolerate split
multiline events. New heads at/above the configured per-message text maximum are
also emitted immediately. Orphan tails bypass storage, including after expiry.

A post-journal successful join preserves the original head identity and appends
the tail's ID and ExtIds. Different journal owners or source formats are not
joined; records carrying acceptance receipts are not joined with another
record. No saturation/expiry/cancellation path publishes a successful ACK.
Unfinished journaled records remain eligible for replay. Pre-journal cancellation
is still not a durable acceptance guarantee.

Reservations remain charged while a head is blocked on downstream handoff and
are released only after handoff, or when the worker relinquishes its runtime
state on cancellation. Closed input drains pending heads in oldest-update order.
The same shutdown context can interrupt a blocked output without a false ACK.

## What is bounded

The estimate covers the whole head, metadata, map entries, identifier/key bytes,
slice capacities and acknowledgement IDs, not just concatenated log text. A
bounded walker visits at most 65,536 values at depth at most 32. Unsupported Go
value types and graphs exceeding those bounds bypass concatenation rather than
being retained with an unknown charge. Cycles terminate at those bounds.
Text and ACK slice capacity growth is reserved **before** allocating new buffers.
An empty tail still needs an ACK-ID reservation in the post-journal stage.

The `concatenation` monitor entry exposes aggregate `pendingMessages`,
`pendingBytes`, `maxPendingMessages`, `maxPendingBytes`, and `budgetRefusals`.
No untrusted identifier is a metric label. Counters are process-local.

These are conservative logical accounting units, **not an exact Go heap/RSS
quota**. Go allocator/map overhead, shared or sliced backing allocations, input
queues, transient incoming records, pools and downstream buffers are separate.
Maintain ingress byte/connection limits, constrained worker/queue sizes, process
memory limits and backpressure. Bypassing concatenation does not by itself bound
the size of a record already admitted by a different ingress.

## Expiry work and compatibility

An update-ordered queue replaces a full map scan on each timer tick. Each
scheduling turn examines/emits at most 32 expired entries, with a timer interval
at most 40 ms (or one quarter of the receiver wait, when smaller). Expiry is a
minimum wait, not a hard delivery deadline: at the default 1,024-message ceiling,
a simultaneous expiry burst can take up to 32 timer turns to drain before
scheduler/downstream delay; increasing the ceiling increases that tail delay.
Blocked downstream consumers retain normal backpressure. Final close drains the
finite remainder; cancellation releases it without acknowledging durable work.

Normal tag/identifier isolation, string/byte-string identifiers, no implicit
newline insertion, head replacement, text-length flush, and journal replay
identity remain covered by behavior tests. Budget saturation intentionally splits
records which previously could remain pending without an aggregate bound.
