# End-to-end load framework and measured optimizations

## Scope and identities

Baseline: merged master `828d34ab1460820cf732810d01b56985d46a4ab9`.
Measured optimized implementation: `910769f010abd3abbbf5338d24e319a3445afb85`,
source tree `9bfe745f07b0ce101f240d757fefc68dd3e7125f`. Later commits add runners,
CI and results, not different production behavior. No dependency upgrade,
default-branch write or deployment is part of this change.

The standalone standard-library Go driver imports no application packages. It
starts the ordinary executable, uses real HTTP and journal files, and checks
original payloads at independent local simulated destinations. It supports
NDJSON, CloudEvents structured JSON, OTLP JSON logs/metrics/traces, and a five-way
mixture. This load matrix does not certify Fluent TCP, Kafka, Elasticsearch,
protobuf, all CloudEvents modes, or actual backend performance. Existing
protocol/crash/Collector suites remain separate.

## Reproduce

Linux, Go 1.27.1, Python 3 and `getconf` are required. Use fresh artifact directories
and run trials serially, without concurrent compilation, profiling or race tests.
Use the same driver and configuration for both binaries.

```sh
go build -mod=readonly -o /tmp/loadtest ./tests/loadtest
go build -mod=readonly -o /tmp/go-fluentd-candidate .
git worktree add --detach /tmp/go-fluentd-baseline-src 828d34ab1460820cf732810d01b56985d46a4ab9
(cd /tmp/go-fluentd-baseline-src && go build -mod=readonly -o /tmp/go-fluentd-baseline .)

GOMAXPROCS=4 python3 tests/loadtest/compare.py \
  --driver /tmp/loadtest --baseline /tmp/go-fluentd-baseline \
  --candidate /tmp/go-fluentd-candidate --pairs 6 \
  --cases tests/loadtest/cases/paired.json --out /tmp/paired-load

GOMAXPROCS=4 python3 tests/loadtest/sweep.py \
  --driver /tmp/loadtest --baseline /tmp/go-fluentd-baseline \
  --candidate /tmp/go-fluentd-candidate --rates 100,300,600,1000,1500 \
  --repeats 2 --requests 1024 --p99-ms 100 --out /tmp/rate-sweep

# Longer horizons must be measured, not extrapolated from short bursts.
GOMAXPROCS=4 python3 tests/loadtest/sweep.py \
  --driver /tmp/loadtest --baseline /tmp/go-fluentd-baseline \
  --candidate /tmp/go-fluentd-candidate --rates 100,300,600 \
  --repeats 2 --seconds 30 --p99-ms 100 --out /tmp/long-load

/tmp/loadtest --audit-only /tmp/paired-load/logs-4096-c64-0-candidate
```

The retained results use six pairs for four profiles and three NDJSON pairs in
separate serial campaigns. The example requests six for every profile.
`--destinations`, `--sink-delay`, `--concurrency`, `--payload`, `--group`, `--batch`,
`--wal-gzip` and `--wire-gzip` vary pressure. `--profile diagnostic` is a separate
five-second CPU profile. Profiled and race binaries are not capacity evidence.

## Measurement contract

Each mock checks exact event fields or uncompressed OTLP bytes, identity, route,
content type and credential. Every required destination must receive each record.
Mock acknowledgement is simulated acceptance, **not downstream fsync**.

Admission latency ends at the HTTP response. End-to-end latency ends at the
slowest required destination's validation, not an application counter. This can
precede final local destination-receipt persistence. Scheduled end-to-end latency
also includes generator dispatch delay. These are monotonic-clock measurements.

Closed-loop concurrency measures burst admission plus complete drain. Open-loop
due times do not shift when responses slow down. Exhausted generator slots produce
explicit unsent dropped arrivals; no silent retry or omission is allowed. Drops,
request errors and missing deliveries invalidate a successful-capacity claim.
An unsent generator arrival is not an accepted message lost by the application.

Application CPU comes from `/proc/<pid>/stat`; generator CPU is reported separately.
RSS/HWM and threads are observed every 20 ms. CPU excludes startup/shutdown, but
peak RSS includes startup/warmup. `TotalAlloc` is allocation churn, **not live RAM**.
Heap diagnostics are collected at measurement boundaries. Saved requests, resource
samples, settings, binary hashes, process exits and errors are retained.

The offline auditor regenerates caller identities and recomputes latency,
throughput and CPU/RSS fields. Negative tests reject missing destinations, wrong
acknowledgements, changed payload/routes, concealed dropped arrivals and forged
performance/exit records. Saved timestamps are not cryptographic proof of a past
peer; payload validation occurs in the independent live sink.

References: [Go diagnostics](https://go.dev/doc/diagnostics),
[open/closed models](https://grafana.com/docs/k6/latest/using-k6/scenarios/concepts/open-vs-closed/).

## Local paired measurements

Linux amd64; Intel Xeon Platinum 8573C virtual CPU exposure; **4-core cgroup quota,
4 GiB memory limit**; `GOMAXPROCS=4`; default GC; overlay filesystem. Driver and
application share this host. It is not dedicated or production-characterized
hardware. Every trial uses fresh storage and retains its warmup receipts.

Binary SHA-256 values:

- Baseline: `78e25164a35d60a9d62f544ff753c401cef3a79c4cdb5322f7f44f27c03efa02`.
- Candidate: `6c951d1f5603d4b4bfcb5b824302cfaa7d16bf58385be04d6b1f54465680ab75`.

Local reconstructed commit metadata differs from GitHub metadata; the complete
measured implementation tree is identical. Native dependencies are unchanged.
Both binaries use queues of 1,024, the same workers, per-record synchronization,
sender batch size one and **10 ms replay cadence**, except the explicit default
one-second row. Payload sizes exclude protocol overhead; each request holds one
item. All **54 final paired trials** pass delivery and graceful-exit checks.

Medians, baseline -> candidate:

| Closed-loop workload | Pairs | Delivered requests/s | Destination P99 ms | CPU us/request | Peak RSS MiB | TotalAlloc MiB |
|---|---:|---:|---:|---:|---:|---:|
| OTLP logs, 4,096 requests, concurrency 64, 512 B | 6 | 300.18 -> 1,489.26 | 13,075.24 -> 2,377.40 | 3,186.04 -> 664.06 | 50.97 -> 48.22 | 1,276.53 -> 173.17 |
| Five-way mixture, 2,048 requests, concurrency 64, 512 B | 6 | 907.77 -> 1,799.97 | 2,029.74 -> 866.70 | 1,074.22 -> 554.20 | 73.37 -> 59.98 | 174.33 -> 76.96 |
| OTLP logs, 1,024 requests, concurrency 64, 8 KiB, wire/WAL gzip | 6 | 383.38 -> 854.11 | 2,401.14 -> 1,000.61 | 2,587.89 -> 1,279.30 | 78.48 -> 84.32 | 1,427.34 -> 317.21 |
| OTLP logs, 384 requests, concurrency 32, default cadence | 6 | 60.86 -> 316.40 | 6,264.33 -> 1,174.04 | 950.52 -> 690.10 | 44.19 -> 48.96 | 32.66 -> 22.76 |

In every pair of these four profiles, throughput improves and destination P99 and
CPU/request fall. Throughput ratios span 2.97-6.02x, 1.70-2.75x, 1.54-2.59x and
4.53-5.39x respectively: host noise is visible, not averaged away.

**Regressions/trade-offs remain:** mixed admission P99 rises from 18.30 to 26.75 ms;
gzip peak RSS rises 7.45%; default-cadence RSS rises 10.80%. Small log RSS gains
are not consistently positive in every pair. At 100 offered requests/s, candidate
1,024-request trials allocate **6,245-6,256 MiB**, versus baseline 5,644-5,792 MiB.
Existing journal encoder/rotation buffers still create substantial low-rate churn.

### NDJSON saturation cliff, not universal 319x throughput

Three more pairs send 2,048 NDJSON requests at concurrency 64. Baseline admission
P99 is 11.87 ms, but destination P99 is **62,950.68 ms**: full live queues fall back
to periodic WAL replay. With pressure at both queue boundaries, destination P99
is **115.98 ms**, admission P99 9.58 ms, drain throughput 32.48 -> 10,357.69/s,
CPU 8,618.16 -> 175.78 us/request and peak RSS 78.59 -> 47.55 MiB.

The roughly 319x ratio removes a specific 60-second recovery wait, not a 319x
steady-state transport cost. Both services are enabled; idle baseline OTLP during
that wait also allocates about 40 GiB, versus 31.23 MiB in the short corrected run.
That is cumulative allocation, not 40 GiB of resident memory.

## Fixed-rate results: no sustainable-capacity certificate

The chosen objective is no drops/errors/missing records, scheduled P99 <= 100 ms,
and delivered rate >= 95% of offered rate. In two **1,024-request** repetitions,
the highest tested point passing both is baseline 100/s and candidate 600/s.
Baseline 300/s drops 29 arrivals in one trial; candidate 1,000/s drops 60 in one.
At 1,500/s, candidate delivery of 1,324-1,349/s misses the rate objective even
though P99 is just below 100 ms.

Longer measurements invalidate extrapolation:

| 30-second offered workload | Two repetitions |
|---|---|
| Baseline 300/s, 9,000 arrivals | 115-137/s after drain; P99 35.07-46.71 s; one run drops 104 arrivals |
| Candidate 600/s, 18,000 arrivals | 591-598/s; P99 472-532 ms; drops 204 and 64 arrivals |
| Baseline 100/s, 3,000 arrivals | No drops, but P99 41 and 464 ms |
| Candidate 300/s, 9,000 arrivals | P99 98 and 25 ms; first run drops 18 arrivals |

All accepted records in these final long trials are accounted for, but **no
repeatable 30-second 100 ms capacity envelope is certified**. Invalid samples
remain in the data. Do not replace this conclusion with the best observed run.

## Optimization sequence and stopping decision

1. Separate CPU profiling found **66.4% cumulative CPU in `filepath.glob`** during
   receipt lookup. Streaming enumeration improved three exploratory pairs but
   retained the repeated whole-directory cost.
2. Inventory only uncertain `.pending-*` identities once at startup. Completed
   message states remain on disk. Directory identity is checked, old receipt
   formats stay compatible, and Sync/no-replace/directory-Sync barriers remain.
3. Reuse gzip workspace, reset away from output buffers before pooling, and test
   concurrent compression plus independence of earlier returned bytes.
4. Drain healthy full replay batches without an artificial retry pause. Unresolved
   batches keep their delay. Coalesced durable-admission notifications wake idle
   replay instead of constantly allocating empty rotated journals; storage-health
   and lost-wakeup behavior remain guarded.
5. Preserve reliable live delivery with cancelable bounded pressure at journal
   output and event sender input. Best-effort and explicit discard behavior remain.
   A slow required destination can propagate admission pressure/head-of-line delay.
6. Existing cancellation-during-Sync tests caught an initial optimization defect.
   Immediate publication after a successful barrier now wins when the queue is
   available. Original assertions remain; failed attempts are retained separately.

Three paired GOGC=50 probes reduce RSS but worsen throughput and CPU in every pair.
Three GOGC=200 probes raise RSS 23-53% in every pair; throughput improves once and
falls twice. Both driver and application inherit the probe environment, so these
are not application-only GC isolation. Neither meets a repeatable improvement
without a resource/latency trade-off; runtime defaults are unchanged.

This is the stopping point for the explored changes, **not global optimality**.
Low-rate/long-run tail behavior, smaller upstream journal buffers and receipt
compaction remain future optimization areas. We have not established that no
other algorithm can improve performance.

## Validation and permanent protection

Final local native full suite: **896 test/subtest passes**. Three shuffled full
race repetitions: **2,688 passes**, no failures/skipped tests. Module verification,
build and vet pass. The existing combined event/OTLP process campaign passes with
plain/gzip WALs, 40 items, two actual SIGKILL/reopen cases, exact recovery and new
work. No durability barrier, payload validation or original test gate is removed.

The [read-only load workflow](../.github/workflows/e2e-load.yml) builds the pinned
baseline and ordinary candidate, checks the oracle separately under race, then
runs three paired trials for each of three CI profiles plus an open-loop run.
Delivery/audit failures are hard gates; hardware-specific percentage improvements
are reported rather than used as flaky pass thresholds.

First inspected hosted [run 36147346029](https://github.com/Laisky/go-fluentd/actions/runs/36147346029):
artifact 10869402412, SHA-256
`04bc0c073f0638072dfe25ae5486f0e5010b9f42b7475c882007d437006d9875`.
All 205 manifest files verify; source tree
`4006eb62b7196a33baeb7139502f617631f76877` matches optimized production files.
Re-audited 18 paired trials and one open-loop run. Hosted medians improve logs
234.97 -> 415.27/s, mixture 641.59 -> 889.95/s and gzip 323.98 -> 372.89/s, with
lower CPU and destination P99. Different workloads/hosts: do not pool those
samples with the local table or assume identical improvement factors.

Retained numerical data: [54 paired trials](../tests/loadtest/results/20260925-paired.csv),
[20 short-rate trials](../tests/loadtest/results/20260925-capacity.csv),
[20 longer/tuning trials](../tests/loadtest/results/20260925-probes.csv).
CSV `passed` means valid delivery accounting, **not the 100 ms SLO**. These 94
trials contain 88 valid and six invalid results; CSV is rounded to six decimals,
while original JSON retains precision. Interrupted, profiled and intermediate
runs are separately retained, not counted in final paired medians.

Startup inventory remains O(retained files) and uses O(uncertain identities)
memory; there is no new hard aggregate quota. Receipts still have no compaction
or TTL. Preserve generation/WAL/receipts together. No exactly-once, physical-power-
loss, production-backend durability or sustainable-capacity guarantee is claimed.

## Follow-up: sustained profile-directed event tuning

[The sustained profiling increment](sustained-profiling.md) uses PR18 head
`26e334332b6920c10d4a38411d715fe9058a4239` as its new baseline, bounded end-to-end
concurrency, CPU/heap profiles and separate unprofiled pairs. Its results must not
be pooled with the older master comparison or interpreted as a fixed-rate SLO.
