# Component, journal and full-process performance

The suite measures actual work with correctness assertions. Throughput is never allowed to stand in for delivery evidence. See [the performance ledger](../../docs/performance.md) and the independent [delivery contract](../delivery/CONTRACT.md).

## Repeat the measurements

Go 1.27 and Python 3 are required. The process benchmark uses Linux `/proc` and local HTTP sockets. It needs no external service or Python package.

```sh
export GOMAXPROCS=2
base=d5b3e67fcf22daad1e6c667b66277bc8bbb29b7c
git worktree add --detach /tmp/go-fluentd-before "$base"
mkdir -p /tmp/go-fluentd-before/tests/performance
cp tests/performance/*_test.go /tmp/go-fluentd-before/tests/performance/
(cd /tmp/go-fluentd-before && go test -mod=readonly -c -o /tmp/bench-before.test ./tests/performance)
go test -mod=readonly -c -o /tmp/bench-after.test ./tests/performance
python3 -m unittest discover -s tests/performance -p 'test_*.py'
python3 tests/performance/compare.py --before /tmp/bench-before.test \
  --after /tmp/bench-after.test --output /tmp/performance-components --repeats 5

go build -mod=readonly -o /tmp/go-fluentd-after .
python3 tests/performance/pipeline.py --binary /tmp/go-fluentd-after \
  --output /tmp/performance-pipeline --count 1024 --repeats 3 --concurrency 1 16
```

Use a new output directory for each campaign. Permanent `performance.yml` CI runs both complete application versions, alternating before/after order, with identical harness files and workload settings on the same runner. It records toolchain, CPU, filesystem, exact refs, binary hashes, every raw measurement, CPU/allocation profiles and independently persisted input/output evidence.

## What the 35 component workloads measure

| Boundary | Included work and caveats |
|---|---|
| Acceptor/post filters | Fresh mutable message construction plus filtering; canonical and byte/dotted fields are separate cases. |
| Parser | Real worker receives a fresh JSON message, parses, flattens and returns it through channels. |
| Concatenator | Two input lines form a checked output, including original confirmation IDs; work unit is two messages. |
| Dispatcher / producer | Actual routing/collector workers; producer has one or two controlled acknowledging senders. This is not a network or multi-inflight saturation test. |
| HTTP receiver | Real Gin handler with signature/time/body validation and queue acceptance; not a durable-acceptance benchmark. |
| HTTP/ES sender | A local peer receives and decodes actual batch requests; transport, compression, and harness waits are included. Not a real cluster's capacity. |
| Fluent encoder | Wire encoding of batches 1, 64 and 512; output goes to `io.Discard`, so no network durability claim. |
| Monitor | Scraping callbacks, JSON encoding, and HTTP recorder; work unit is a scrape, not a message. |
| Pending monitor | Zero, 1,000 and 100,000 genuinely partially acknowledged fanout messages. State is produced by sending messages and withholding one sink's result, not by writing private maps. |
| Journal append | Plain/gzip, distinct per-record, per-64-record and final-only Sync policies. Each policy is compared only with itself. Stream flushes and final Sync remain timed. |
| Journal confirmation | Unique ID recording plus final Sync and membership checks; plain/gzip separately. |
| Journal recovery | First-pass public high-water scan over 64/4,096 retained records, including unacknowledged data. Time is per scan, not per record. |
| Journal replay | Scan 1,024 records with half confirmed; verify the other 512 and their payloads. Normalization counts all 1,024 scanned messages. |
| Confirmation cache | Hit, miss, and refreshed deadline over a fixed 65,536-key set; does not measure indefinite new-ID growth. |

`ns/op`, `B/op` and `allocs/op` always describe the complete operation. The reported `msgs/op` or `scrapes/op` supplies the normalization denominator. The reporter rejects missing workloads, inconsistent units, non-finite measurements, and missing/extra repetitions. CPU/transformation loops use 100 ms per sample; append and confirmation loops use 4,096 operations; recovery/replay use 20 passes. Fixed-count disk loops bound file sizes rather than permitting adaptive calibration to create unbounded WALs.

Common data contains about 2 KiB of highly compressible text and ordinary metadata. These are representative shapes, not a claim that real-world entropy or message-size distributions match. Recovery measurements use cached local files and include decoder allocations; they are not cold-device restart timings.

## Full executable workload

`pipeline.py` launches the ordinary, uninstrumented executable. Every request uses durable HTTP acceptance, and each of two independent protocol peers fsyncs its received records. Input encoding and producer manifest fsync are outside the timer; sink decoding/fsync are inside it. After timing, the actual persisted sink files must contain all expected fields, stable event identities, and no missing, extra or duplicate records. No application queue or counter proves delivery.

Each profile uses 1,024 timed events plus a separately excluded warmup event, 2,048 payload characters, sink batches of 64, and plain/gzip WALs at concurrency one and sixteen. Full process CPU seconds and peak RSS are recorded. Request latencies are nearest-rank P50/P95/P99/max, measured through the durable response. Delivered throughput is separately timed until both sinks have all records.

This is closed-loop traffic: it does not establish open-loop overload latency, queueing SLOs, or maximum production capacity. Python client/peer overhead, loopback networking, fsync and filesystem behavior are part of the result. `TCP_NODELAY` avoids measuring a Python-peer Nagle/delayed-ACK interaction as Go processing cost. Some component improvements may have no visible effect here; in particular the ES output path does not use the Fluent wire encoder.

## Reliability and interpretation

Performance binaries are not race-instrumented. Separate unchanged correctness CI runs build/vet, randomized repeated race tests, and all executable delivery contracts. Buffer tuning must pass records beyond 4 MiB; ID tuning must preserve byte-exact offset serialization, truncated-record errors and concurrent writes; cache/gauge changes must preserve cardinality and all-sender confirmation.

Preserve all samples and unfavorable results. Medians and observed ranges do not assert statistical significance. CI fails on incorrect work, missing data, failed tests or delivery mismatches, not on noisy wall-clock thresholds. No optimization removes a flush/Sync, changes durable acceptance, shortens TTL, drops messages, or treats a larger batching policy as an equivalent guarantee. Future group-commit work requires a separately specified latency/durability contract and its own crash/error tests; it is not implemented here.
