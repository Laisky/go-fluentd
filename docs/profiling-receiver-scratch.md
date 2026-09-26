# Profile-guided receiver input scratch reuse

## Scope and decision

Baseline: PR18 `0dc085cf013e99ffaffe2e9ad0c32d8eb5c592d6`, tree
`1919753dbc977d90264ea6ffbb0fa93cfb16e360`. This is the already optimized
HTTP-write-buffer implementation, not old master. The new code/test tree is
`2f373ba5f02975225715d961702a54e9f7f6b6fd`.

Retain this bounded change for the repeatable reduction in **CPU per record and
allocation churn**. It is not a uniform throughput, tail-latency or RSS improvement.
Only **three of six** unprofiled trials pass the unchanged full steady-load gate;
the candidate diagnostic also misses that gate. Those failures are retained,
not rerun away, excluded from medians, or relabeled as qualification successes.
This increment does not establish stable open-loop capacity or a production SLO.

The receiver reuses input scratch only during synchronous body reading and
complete `streamformat.Decode`. It returns the scratch before any message is
published or any durable ACK is awaited. Every returned record owns its decoded
strings/values. No borrowed string, unsafe alias, request-body pooling, change to
journal/ACK/retry semantics, dependency upgrade or runtime tuning is introduced.

Each retained scratch buffer has capacity at most **128 KiB**. Larger requests
remain accepted up to the original configured MaxBytesReader limit, but their
scratch is discarded. This is a per-buffer retention policy, **not a global pool,
connection or process-memory quota**. Concurrent readers and retained pool entries
can increase memory. Low-volume behavior and idle memory are not certified here.

## Hotspot and rejected output-buffer experiment

A fresh 500,000-record baseline diagnostic captured 15 seconds of CPU and two
heap samples after a five-second delay. The trimmed 71-second observation meets
the original qualifier: mean app CPU **56.16%** of four effective cores,
**91.55%** of one-second windows above 50%, rate CV **0.1794**, outstanding work <=64.
Its sampled allocation includes MessagePack strings (23.89%), `readEventBody`
(21.21%, 1,922.40 MiB flat), output-buffer growth (20.48%) and JSON strings (20.31%).
Sampling totals are not exact fixed-workload allocation measurements.

An initial reference-counted outgoing NDJSON body pool preserved the owner and
all transport/GetBody readers until their Close calls. Its lifetime tests passed,
but the wrapper caused the HTTP/TCP copy path to allocate `io.copyBuffer` again,
which offset the pooled output savings. One completed 300,000-record A/B pair
reported **6,871.84 -> 6,872.34 records/s** and less than 1% allocation improvement.
The output pool was rejected; its patches, tests, profiles and both trial records
remain separate evidence. The comparison controller was stopped after that pair;
no partial controller manifest is presented as a completed three-pair campaign.
No outgoing pooling code enters this PR.

The adopted receiver change avoids asynchronous transport lifetime entirely.
`Content-Length` is only a <=64 KiB allocation hint; reads still reach actual EOF
through MaxBytesReader. The same read helper backs the private-storage wrapper
and the pooled decoder. A failed read or invalid trailing record publishes no
prefix, and the next request cannot inherit its suffix.

A fresh 500,000-record candidate profile removes the input-body allocation hotspot,
but is **diagnostic only, not qualified**: 71 steady seconds, mean CPU **53.04%**,
73.24% qualifying windows and CV **0.2088**. The gate requires >=80% windows and
CV <=0.20. An earlier baseline capture started too early and is likewise retained
as an unqualified diagnostic. Neither is counted as a fresh steady A/B result.

## Fixed-workload unprofiled comparison

Linux amd64, Go **1.27.1**, exact native go.mod/go.sum, four-core cgroup quota,
4 GiB memory limit, GOMAXPROCS=4, shared Intel Xeon Platinum 8573C exposure and
overlay filesystem. Driver and app share the host but CPU is measured separately.
No profiling, compilation or tests run concurrently with these timed trials.

Each trial sends **300,000 NDJSON records with 16 KiB Unicode text**, end-to-end
concurrency64, bounded fixtures, ordinary configured application, real plain
journal and per-record Sync, and one independent local mock destination. Both
versions use the identical unchanged driver/settings. Order is **AB/BA/AB**.
Sixty-four warmup records per trial are retained but excluded from measurements.

All **1,800,000 measured requests** were accepted and accounted for, with zero
missing records, request failures or generator drops and six graceful zero exits.
There were **27 identical duplicate observations**: 19 baseline and eight candidate.
This remains at-least-once delivery, not exactly-once. Mock validation does not
certify a backend fsync and can precede final application receipt persistence.

| Metric, median | Baseline | Candidate | Change |
|---|---:|---:|---:|
| Delivered records/s | 6,813.29 | 7,178.21 | +5.36% |
| Application CPU microseconds/record | 317.07 | 295.00 | -6.96% |
| Destination P99 ms | 23.373 | 21.004 | -10.14% |
| Admission P99 ms | 7.445 | 6.242 | -16.16% |
| Peak RSS MiB | 188.83 | 189.63 | +0.42% |
| Cumulative allocated bytes/record | 86,016 | 67,069 | -22.03% |

All three pairs lower CPU/record by **3.32-11.91%** and allocation by
**22.03-22.17%**. Throughput changes are **-1.38%, +4.92%, +13.29%**.
The first pair's destination P99 worsens **27.38%** and admission P99 **2.38%**;
other pairs improve both. RSS pair changes are **-0.30%, -0.35%, +1.67%**:
there is no repeatable resident-memory reduction. All samples remain in the table
and [full-precision CSV](../tests/loadtest/results/20260926-receiver-scratch.csv).
Three pairs on a shared host do not establish statistical confidence. These
ratios of medians are not medians of pairwise ratios; previous hosts/results are
not pooled with this new baseline.

### Every steady-load qualification, unchanged thresholds

The gate trims three seconds at each end and requires >=15 one-second windows,
mean application CPU >=50% of effective capacity, >=80% of windows above50%,
rate CV <=0.20 and outstanding work <=64. CPU fraction is not host load average.

| Pair/version | Steady seconds | Mean CPU | Windows >=50% | Rate CV | Qualified |
|---|---:|---:|---:|---:|---|
| 0 baseline | 38 | 53.96% | 81.58% | 0.1517 | yes |
| 0 candidate | 38 | 50.86% | 63.16% | 0.2557 | no |
| 1 baseline | 37 | 54.10% | 89.19% | 0.1764 | yes |
| 1 candidate | 35 | 52.46% | 77.14% | 0.1848 | no |
| 2 baseline | 41 | 53.65% | 85.37% | 0.2243 | no |
| 2 candidate | 35 | 53.69% | 91.43% | 0.1681 | yes |

Every trial has mean CPU above50% and bounded work, but that alone is **not**
passing the full stability gate. CSV `passed` means correct delivery accounting,
not stable-load qualification. No trial or threshold was replaced after seeing
results. The next profiling round needs a separately calibrated workload; its
parameters must be frozen before a new comparison, not retrofitted to these data.

## Native behavior, ownership and recovery

- Complete suite: **1,127 test/subtest passes**, no failures or skips.
- Three shuffled complete race runs: **3,381 passes**, no failures/skips/race reports.
- Differential scratch fuzz: **13,193 executions**, passed.
- Native module verification, application build, vet and formatting passed.
- Actual race-instrumented executable: plain/gzip mixed-pipeline recovery passed,
  **40 items**, two SIGKILL/reopens, exact recovery, fresh work and clean exits.
- Five ownership fixtures cover NDJSON, structured/batch CloudEvents and binary
  JSON/opaque bodies. Tests immediately overwrite recycled scratch before
  examining all returned values, including nested data and 64-bit integers.
- Twenty-four concurrent workers, invalid suffixes/duplicate keys/surrogates,
  count/size limits, lying/missing length hints, failed-read reuse and retention
  caps are covered. The original receiver durable-ACK tests remain unchanged.
- Two deliberately unsafe variants are rejected by their named assertions:
  borrowed input strings and retention of oversized scratch. Each includes
  passing before/restored runs and an independent body-read positive control.
  Compilation errors, no-test results and timeouts are not accepted as detection.

The new benchmark compares identical decoding with private versus pooled input
scratch. At16KiB it reports approximately **35,736 ->17,309-17,315 B/op**,16->15
allocations. At512B it reports2,584->1,432 B/op. A256KiB case exceeds retention:
no meaningful allocation savings, and pool/New overhead can add allocations.
These are mechanism controls, not a substitute for application E2E CPU/RSS.

A first focused invocation had a test import naming conflict and did not compile;
it was corrected before measurement/full acceptance. Failed experiments are
retained separately. The candidate binary was built before the final added fuzz
function; production files are identical. Dependencies, driver, sender transport,
JSON tokenizer, synchronization barriers and all old measurements are unchanged.

## Reproduction and evidence boundaries

Use the same driver for both binaries, fresh output directories and serial trials:

```sh
go build -mod=readonly -o /tmp/loadtest ./tests/loadtest
go build -mod=readonly -o /tmp/go-fluentd-candidate .
# Build /tmp/go-fluentd-baseline from immutable 0dc085cf with native dependencies.
GOMAXPROCS=4 python3 tests/loadtest/compare.py \
  --driver /tmp/loadtest --baseline /tmp/go-fluentd-baseline \
  --candidate /tmp/go-fluentd-candidate --pairs 3 \
  --cases tests/loadtest/cases/receiver-scratch.json --out /tmp/receiver-scratch
python3 tests/loadtest/sustained.py /tmp/receiver-scratch/ndjson16k-c64-0-candidate --report-only
```

The existing read-only hosted workflows run correctness, recovery and smaller
routine load cases. They do **not** independently reproduce this1.8-million-request
local campaign. Record exact-head CI separately. Raw CPU/heap profiles, all six
request/resource ledgers, source snapshots, negative controls and the rejected
output experiment are retained in the continuation evidence package. Bulk WAL
files are excluded; the independent request/delivery and process records remain.

The retained pool may contain old body bytes internally until reuse or GC; it is
not a memory-erasure mechanism. Do not turn returned values into input aliases
in future decoder changes. No aggregate storage quota, receipt compaction/TTL,
physical-power-loss qualification, uniform speedup or global optimality is claimed.
