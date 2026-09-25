# Sustained, profile-directed event optimization

## Baseline and measurement rules

Continue PR #18, branch `perf/e2e-load-20260925`. The baseline for this increment
is **26e334332b6920c10d4a38411d715fe9058a4239**, not the older master used by
[the initial performance report](e2e-performance.md). Existing measurements are
not overwritten. This increment targets the NDJSON/CloudEvents JSON event path;
it does not claim the same improvement for OTLP or every payload distribution.

The workload uses the ordinary application executable, HTTP, real synchronized
journals and independent local mock destinations. The driver imports no
application packages. Both versions use the same driver, configuration and
regenerated request identities. A successful HTTP ACK is **not** the end of a
request's load-generator slot: `--delivery-window` waits until **every required
destination** validates its content. This bounds accepted-but-not-delivered work
and prevents an ever-growing backlog from masquerading as a steady workload.
Destination arrival is not a physical downstream fsync or final local receipt.

`--bounded-fixtures` retains identities and per-request timing records, not all
request bodies. Templates must reproduce the original fixture byte for byte;
request payload memory scales with concurrency, while retained measurement
metadata still scales with request count. The mock recognizes exact generated
valid bytes by hash, with independent canonical JSON validation for alternate
serialization. These driver changes are used for both versions and are **not**
counted as application optimizations.

App-only CPU is measured separately from driver CPU. CPU utilization divides
used app cores by the smaller of the visible CPU count and the cgroup CPU quota.
For this measured environment that is four cores, not the host's exposed hardware
thread count. Qualification trims three seconds at both ends and requires:

- At least 15 complete one-second windows, mean app CPU at least 50% of quota,
  and at least 80% of those windows individually at or above 50%.
- Delivered-rate coefficient of variation no greater than 0.20; no failed,
  missing or dropped arrivals; observed outstanding work no greater than the
  configured end-to-end concurrency window.
- For a profiled run, the entire CPU/heap capture window lies after warmup and
  before the drain/idle tail. Invalid qualification is retained, not retried away.

The CPU threshold qualifies a **profiling workload**. It is not a claim about a
fixed-rate production capacity SLO, nor a requirement to waste saved CPU in an
optimized candidate. The workload is closed-loop saturation with bounded work,
not an open-loop rate guarantee. CPU and throughput can vary on a shared host.

## Reproduce without combining diagnostics and timing

Linux, the repository's Go toolchain and Python 3 are required. Run trials
serially. Do not compile or run race tests concurrently with measured trials.
Use new output directories and preserve failed runs.

```sh
go build -mod=readonly -o /tmp/load-driver ./tests/loadtest
go build -mod=readonly -o /tmp/event-candidate .
git worktree add --detach /tmp/event-baseline-src 26e334332b6920c10d4a38411d715fe9058a4239
(cd /tmp/event-baseline-src && go build -mod=readonly -o /tmp/event-baseline .)

# First establish a qualified workload and capture the real application's CPU
# and heap snapshots. No forced GC is requested by the profiler.
GOMAXPROCS=4 /tmp/load-driver --binary /tmp/event-baseline \
  --out /tmp/event-baseline-profile --protocol ndjson --requests 150000 \
  --concurrency 64 --payload 16384 --bounded-fixtures --delivery-window \
  --profile cpu --profile-delay 5s --profile-seconds 15 --timeout 45s
/tmp/load-driver --audit-only /tmp/event-baseline-profile
python3 tests/loadtest/sustained.py /tmp/event-baseline-profile

go tool pprof -top /tmp/event-baseline /tmp/event-baseline-profile/cpu.pprof
go tool pprof -top -alloc_space \
  -base /tmp/event-baseline-profile/heap-start.pprof \
  /tmp/event-baseline /tmp/event-baseline-profile/heap-end.pprof
go tool pprof -top -inuse_space \
  /tmp/event-baseline /tmp/event-baseline-profile/heap-end.pprof

# Use the fixed workload unchanged for unprofiled AB, BA, AB pairs.
GOMAXPROCS=4 python3 tests/loadtest/compare.py \
  --driver /tmp/load-driver --baseline /tmp/event-baseline \
  --candidate /tmp/event-candidate --pairs 3 \
  --cases tests/loadtest/cases/sustained.json --out /tmp/event-steady-pairs

# Independently inspect each sample's CPU occupancy and variability. A failed
# CPU-occupancy gate must not invalidate a genuine same-workload CPU reduction.
python3 tests/loadtest/sustained.py \
  /tmp/event-steady-pairs/steady-ndjson-16k-c64-0-candidate --report-only
(cd tests/loadtest && python3 -m unittest -v test_sustained)
```

`cpu.pprof` samples execution, not time blocked waiting for I/O or a mutex. Heap
`alloc_space` differences estimate sampled allocation churn between snapshots;
`inuse_space` reports live Go heap as observed by GC, not process RSS. Heap
profiles can lag collection. The driver separately preserves RSS/HWM and resource
samples. See [Go diagnostics](https://go.dev/doc/diagnostics) and
[pprof](https://pkg.go.dev/runtime/pprof). Profiled runs are excluded by the paired
runner, so profiling overhead cannot create a claimed speedup.

## Hotspots, attempted changes and correctness boundaries

The first **qualified** baseline contained 31 steady seconds, average app CPU
60.3% of the four-core quota, every one-second window above 50%, delivered-rate
CV 0.170, and at most 64 outstanding requests. During its 15-second CPU profile:

1. JSON tokenizer buffer growth accounted for **31.5% of sampled allocation**.
   Merely changing `bytes.Reader` to `bytes.Buffer` failed to remove it: Go's
   compatibility decoder deliberately hides the buffer's direct-input path.
   The adopted change uses the **standard-library `encoding/json/jsontext`
   tokenizer**, keeping the existing domain conversion, integer precision,
   duplicate-key, nesting, UTF-8 and surrogate checks. It introduces no unsafe
   string alias and no dependency change. Caller input remains unmodified and
   returned strings remain independent after that input is overwritten.
2. After that change, a fresh qualified run put **`io.ReadAll` at 28.4% of sampled
   allocation**. The receiver now preallocates private input storage only for
   positive length hints at most 64 KiB, with spare EOF-read capacity. It still
   reads to actual EOF through `http.MaxBytesReader`. False/unknown length hints,
   fragmented reads, I/O errors and size-limit failures preserve their behavior.
3. The surrogate escape scanner consumed roughly **7–8% of CPU** in the first
   two profiles. A standard-library byte search skips the scalar scan only when
   the body contains **no backslash**. UTF-8 validation and JSON parsing still
   run; any backslash takes the original scanner. Differential fuzzing compares
   both scanners on arbitrary byte strings.

The isolated 16 KiB parser benchmark changed from roughly **82,848 B/op and 27
allocations** to **17,216 B/op and 15 allocations**. The request-body reader
changed from **37,808 B/op / 14 allocations** to **18,480 B/op / 2 allocations**.
These are microbenchmarks explaining mechanism, not substitutes for E2E results.
The failed buffer-only attempt and raw benchmark samples remain in the evidence.

A third intermediate profile after request-body preallocation preserved all
150,000 deliveries but **failed the fixed stability gate**: mean CPU 54.9%, only
79.3% of windows over 50%, delivered-rate CV 0.263. It remains diagnostic evidence,
not a qualified capacity result. No threshold was weakened to make it pass.

## Scope and operational limits

No sync, payload check, retry obligation, journal format or durability barrier is
removed. Existing event/OTLP coexistence and crash tests remain required. Original
README architecture images and the previous 94-trial data remain unchanged.

Profiles and paired experiments characterize this synthetic 16 KiB event workload
on this host. They do not certify sustainable offered-rate capacity, power-loss
recovery, exactly-once delivery, arbitrary JSON distributions or all transports.
Future profiling must reevaluate the new hotspots rather than keep optimizing an
old ranking. When utilization falls below 50%, report that result and separately
calibrate the next diagnostic workload; do not silently increase the candidate's
work or pool it into the fixed-workload A/B comparison.

## Unprofiled incremental E2E result

Three alternating pairs (AB, BA, AB), 150,000 measured requests per trial,
concurrency 64, 16 KiB text, plain WAL, per-record synchronization, one required
local destination, four-core cgroup quota, 4 GiB memory limit, Go 1.27.1 and
`GOMAXPROCS=4`. Each trial uses new storage and 64 untimed warmup records. All
**900,000 measured requests** were accepted and validated at the destination,
with zero missing records, errors, generator drops or duplicates. All processes
exited gracefully. Profiling was disabled for all six trials.

| Metric | PR18 baseline | Three changes combined | Change in medians |
|---|---:|---:|---:|
| Delivered records/s | 4,006.42 | 5,116.92 | +27.72% |
| App CPU, microseconds/record | 588.87 | 418.33 | -28.96% |
| Destination P99, ms | 42.374 | 28.202 | -33.45% |
| HTTP admission P99, ms | 10.519 | 8.121 | -22.80% |
| Peak process RSS, MiB | 150.35 | 133.44 | -11.24% |
| Cumulative allocation, bytes/record | 205,379 | 117,031 | -43.02% |

Each pair improves all six reported metrics. Paired throughput improvement ranges
from 26.1% to 45.3%; paired RSS reduction ranges from 7.3% to 15.3%. Ratios of
medians and medians of paired ratios are different statistics and should not be
interchanged. The third baseline had a pronounced P99 excursion (121.73 ms),
versus 37.88 and 42.37 ms in the first two. It is retained rather than discarded;
these three pairs do not establish a confidence interval or a production SLO.

In another post-change diagnostic at the original concurrency, mean CPU was
52.4%, but only 66.7% of windows exceeded 50% and delivered-rate CV was 0.211:
**it failed the unchanged qualification gate**. A separately calibrated
200,000-request/concurrency-128 diagnostic reached mean CPU 55.1%, with 97.1% of
windows above 50%, but CV 0.209 still narrowly exceeded 0.20. It too is retained
as diagnostic-only, and is not included in the six-trial comparison. The two
qualified earlier profiles establish the initial hot paths; later variability
must not be concealed by changing thresholds or declaring all runs stable.

The new CPU profile is dominated by syscall and JSON output/GC work. MessagePack
string allocation and output buffers are the next allocation areas to inspect.
The old scalar escape scan and duplicate decoder-buffer growth are no longer
among the leading reported nodes. Further optimization must preserve ownership,
validation and synchronization; this increment does not assert global optimality.

## Validation performed on this increment

The exact native module graph and Go 1.27.1 were used without replacements.
Module verification, full build, vet and formatting passed. The final ordinary
suite recorded **1,090 test/subtest passes**; three shuffled full-suite race runs
recorded **3,270 passes**, without failures, skipped tests or race warnings.
Two actual race-instrumented executable coexistence scenarios (plain/gzip WAL)
recovered all 40 accepted event/OTLP items after SIGKILL and then accepted fresh
work, finishing with clean SIGTERM. JSON/tokenizer differential fuzzing completed
55,657 executions and escape-scan differential fuzzing 286,286 executions.

The existing CI oracle phase also runs the lightweight steady-gate unit tests;
large profiled workloads remain explicit, serial diagnostic runs rather than a
flaky hardware-percentage gate on every PR. Publication and hosted CI must be
checked separately from this local acceptance. The source patches, binary hashes,
raw profile samples, resource observations, all paired requests, qualification
failures and complete test logs are retained in the accompanying evidence.

The unprofiled trials were also checked against the unchanged profiling-workload
qualifier: **four of six qualify**. Pair 1's optimized sample has 78.3% (rather
than 80%) of windows above 50% CPU, although its throughput CV is 0.165. Pair 2's
baseline has throughput CV 0.339 and only 69.2% of windows above 50%. Both remain
in the reported medians. All six satisfy delivery correctness and the bounded
window, but the result must not be described as three universally stable pairs.
The first complete pair qualifies on both sides; larger, independently isolated
campaigns are needed for a robust variance/confidence assessment.
