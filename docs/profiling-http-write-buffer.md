# Profile-guided HTTP event write buffering

## Scope and preserved baseline

This increment starts from PR18 `e4525f7acdc41ce20900a8b2a13e9c72ba1222ce`
(tree `4e0535976f1f8022539b053ab167898a799d1a4b`), not old master.
Production changes are limited to a 32 KiB `http.Transport.WriteBufferSize` in
`internal/senders/http_events.go`. The previous JSON, journal and gzip optimizations,
dependencies, original README diagram and historical measurements remain intact.

The existing default transport buffer is 4 KiB. In this Go 1.27.1 HTTP/1 path,
`transferWriter.doBodyCopy -> bufio.Writer.ReadFrom -> TCPConn.ReadFrom ->
genericReadFrom` can allocate another temporary copy buffer when the request
outgrows the connection buffer. A 32 KiB connection buffer accommodates the tested
16 KiB record plus headers and avoids that fallback. Request serialization, length,
private body ownership, retry bytes, response handling and durable barriers are
unchanged. This does not introduce an unsafe request-body buffer pool: net/http may
close request bodies asynchronously after Do returns.

References: [Transport](https://pkg.go.dev/net/http#Transport),
[Client.Do](https://pkg.go.dev/net/http#Client.Do),
[Go profiling](https://go.dev/doc/diagnostics).

## Fresh profiles, separate from performance trials

Both completed diagnostics use 500,000 NDJSON records, 16 KiB text, end-to-end
concurrency 64, the unchanged delivery-window/bounded-fixture driver, and a 15-second
CPU profile plus two heap snapshots beginning five seconds after warmup.
The existing steady-load thresholds were not weakened.

| Diagnostic | Steady seconds | Mean application CPU / 4-core quota | Windows >=50% CPU | Rate CV |
|---|---:|---:|---:|---:|
| Baseline | 52 | 59.88% | 98.08% | 0.1640 |
| Candidate | 46 | 58.43% | 95.65% | 0.1748 |

Both qualify and peak outstanding delivery work is 64. Baseline `io.copyBuffer`
accounts for 1,761.60 MiB / 13.16% of sampled allocation in the profile interval.
Its candidate flat allocation is zero in the sampled profile (1.50 MiB / 0.011%
cumulative). Sampling is not an exact byte count. The profiles process different
numbers of records; fixed-count, unprofiled comparisons below establish the gain.

A first combined build/profile command was terminated by its outer timeout, and
a shorter 150,000-record diagnostic finished traffic before CPU capture ended.
Neither is accepted as a qualified profile. A first microbenchmark command was
also interrupted after cold compilation; its partial output is retained separately.
Completed diagnostic/benchmark reruns are explicitly identified, not substituted
for failed performance trials. No timed A/B sample was replaced.

## Fixed-workload, unprofiled E2E comparison

Go 1.27.1/Linux amd64, native dependencies, shared AMD EPYC 9V74 host, four-core
cgroup quota, 4 GiB memory, GOMAXPROCS=4, overlay filesystem. The driver and app
share the host, but CPU is measured separately. Each trial sends 300,000 NDJSON
records with 16 KiB Unicode text, maximum 64 outstanding deliveries, one required
local HTTP mock, a real plain journal and per-record synchronization. All settings
and the exact driver binary are identical between versions. Three serial pairs
run AB/BA/AB, with profiling and concurrent compilation/tests disabled.

| Median | Baseline | Candidate | Change |
|---|---:|---:|---:|
| Delivered records/s | 8,978.63 | 9,429.02 | +5.02% |
| Application CPU microseconds/record | 258.03 | 241.20 | -6.52% |
| Destination P99 ms | 16.947 | 16.395 | -3.25% |
| Admission P99 ms | 5.491 | 5.361 | -2.37% |
| Peak RSS MiB | 199.87 | 186.32 | -6.78% |
| Cumulative allocation bytes/record | 98,550 | 84,821 | -13.93% |

All three pairs improve throughput (0.39-5.02%), CPU/record (2.00-6.52%), RSS
(2.17-10.44%) and allocation (13.35-14.70%). Not every latency improves: pair 1
(zero-based) destination P99 regresses 0.034%, and pair 2 admission P99 regresses
0.359%. Three pairs on a shared host do not establish statistical confidence.
The table is a ratio of medians, not the median of pairwise ratios.

All six trials satisfy the unchanged qualification: 25-28 steady one-second
windows, mean application CPU 55.83-58.03%, 92-100% qualifying windows, rate CV
0.1347-0.1680 and maximum outstanding 64. All 1,800,000 measured requests were
accepted and accounted for, with no missing records, request errors or generator
drops and six clean exits. Fifteen identical retries were observed in one baseline
trial; candidate duplicate count is zero. This is not exactly-once qualification.
Each trial also retains 64 untimed warmup requests.

[All six full-precision rows](../tests/loadtest/results/20260926-http-write-buffer.csv)
and the [workload](../tests/loadtest/cases/http-write-buffer.json) are committed.
Raw records were audited by the existing runner. A separate calculation recomputes
throughput, P99, CPU and allocation from saved requests/resource endpoints and
re-runs every qualification. Results from previous hosts/baselines are not pooled.

## Correctness and small/large controls

Native module verification, build/vet and formatting passed. Full suite:
**1,108 test/subtest passes**; three shuffled whole-suite race runs: **3,324 passes**,
no failed or skipped tests and no race report. Five packages without test files
are not counted as executed tests. Configured race-binary coexistence/recovery
passed for plain and gzip journals: 40 items, two SIGKILL/reopens, exact recovery,
fresh progress and clean shutdown.

The new wire test concurrently sends ten payload sizes from empty through 128 KiB,
including the 4 KiB and 32 KiB boundaries. It requires exact bytes, Content-Length,
credentials and identities, and identical successful retry after a 503. Both plain
HTTP/1.1 and verified-TLS HTTP/2 run through real local servers. A deliberately
changed retry body fails the intended assertions; the correct version and an
independent failure-response control pass.

The diagnostic microbenchmark varies the connection buffer independently on the
same implementation. For a 16 KiB payload, total in-process client-plus-mock
allocation falls from approximately 40,131 to 26,155 B/op, 95 to 93 allocations.
512-byte and 64-KiB controls show no comparable allocation reduction. These are
not application-only CPU measurements or a universal size-independent speedup.

A separate defensive change resolves the outstanding fixture-allocation CodeQL
warning: check `len(body)+64` before addition. Boundary tests use integer limits
without allocating huge slices; normal generated fixtures remain byte-identical.
An upper-bound-check mutant fails its intended overflow assertion while a normal
fixture control passes. The guard was added after measurement; both A/B runs used
the same frozen pre-guard driver. No application gain is attributed to the guard,
and no claim is made that ordinary bounded workloads previously overflowed.

## Reproduction and limits

Build ordinary baseline and candidate applications with the same native Go version.
Use the current driver for BOTH binaries and fresh output paths. From the repo root:

```sh
go build -mod=readonly -o /tmp/driver ./tests/loadtest
go build -mod=readonly -o /tmp/candidate .
# Build /tmp/baseline from the e4525f7a source in a separate worktree.
GOMAXPROCS=4 python3 tests/loadtest/compare.py --driver /tmp/driver \
  --baseline /tmp/baseline --candidate /tmp/candidate --pairs 3 \
  --cases tests/loadtest/cases/http-write-buffer.json --out /tmp/write-buffer-pairs
python3 tests/loadtest/sustained.py /tmp/write-buffer-pairs/ndjson16k-c64-0-baseline
GOMAXPROCS=4 go test -mod=readonly -run '^$' \
  -bench BenchmarkHTTPEventTransportWriteBuffer -benchtime=200ms -count=3 ./internal/senders
```

A larger connection buffer costs 28 KiB more per retained HTTP/1 connection versus
4 KiB. Many idle connections or mostly small records may therefore consume more
memory. This does not add a connection/memory quota. HTTPS/HTTP2 correctness is
covered, but its performance and every other protocol/backend are not certified.
The mock acknowledges validation, not backend fsync; destination arrival can
precede local final receipt persistence. These bounded closed-loop tests do not
establish long-horizon offered-rate capacity or a production SLO.

MessagePack string materialization and encoded-output buffer allocation remain
significant profile targets. They require separate ownership/recovery evidence;
this patch does not use zero-copy aliases or weaken durability to remove them.
Receipts still have no compaction/TTL/aggregate quota. No merge or deployment is
part of this increment. Fresh hosted CI status is recorded on PR18, not inferred
from the preceding head's checks.
