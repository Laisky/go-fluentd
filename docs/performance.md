# Measured performance iterations

## Contract

Compare the same workload, payloads, toolchain, concurrency, storage and durability boundary before and after each change. Performance work must preserve delivery, replay, failed-write handling, and acknowledgement ownership. Do not obtain a faster result by skipping Sync, changing durable acceptance, dropping messages, disabling correctness checks, or changing confirmation TTL.

## Measurement protocol

- Inventory the receiver, acceptor filters, dispatcher/producer, tag filters, post filters, encoder/senders, monitor, and journal boundaries.
- Benchmark CPU-only transformations separately from disk/network operations. Recreate mutable input outside timed sections only when explicitly documented; include per-message reconstruction in normal filter pipeline measurements.
- Journal: buffered append plus final flush, per-record durable append, explicit batch Sync, ID recording, confirmation membership, rotation/recovery; plain and gzip formats. Do not compare different Sync policies as an optimization.
- Record Go version, exact source revisions, GOMAXPROCS, CPU, filesystem, payload sizes, records per operation, repetitions and raw measurements. Container/tmpfs timings are not production SSD predictions.
- Use repeated uninstrumented measurements for performance, and separate CPU/allocation profiles to locate expensive work. Race-instrumented timings are correctness evidence, not throughput measurements.
- For each accepted optimization: add behavior/error controls, benchmark unchanged code, change one independently reviewable concern, rerun the identical benchmark, validate tests/race/full-process delivery, commit and push. Report regressions and uncertain/noisy effects instead of selecting only favorable samples.
- Report ns/op, bytes/op, allocations/op, normalized messages/s; real-pipeline experiments also report accepted/delivered counts and request latency percentiles. Never use accepted-only throughput as delivered throughput.

## Current state

Baseline application: `d5b3e67fcf22daad1e6c667b66277bc8bbb29b7c` (merged delivery-contract PR #10).
Baseline journal: application-pinned recovery revision, with the merged upstream tree checked before modification.

Measurement harness and baseline collection are in progress. No performance improvement is claimed yet. Completed iteration evidence and the next remaining bottleneck will replace this paragraph as work proceeds.
