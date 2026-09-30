# OTLP edge admission and recovery measurements

These are isolated **Mac development measurements**, not qualification of b1 or
the home Collector. They were made on Apple M1, Darwin arm64, Go 1.27.0, an APFS
temporary directory, and GOMAXPROCS=8. Other development work may introduce
variance. No production state, credentials, endpoints or paid model calls were
used. Fixed 256-operation runs bound the test WAL; setup/shutdown is not timed.

## Measured issue and admission safeguards

Before admission scan budgets, two repetitions measured:

| Retained root files | Admission clients | Durable envelopes/second | Allocation bytes/envelope |
|---|---:|---:|---:|
| 0 | 1 | 82–91 | 20,724–20,740 |
| 0 | 8 | 71–96 | 20,766–20,797 |
| 1,000 | 1 | 88–90 | about 646,000 |
| 10,000 | 1 | 17 | about 6,276,000 |
| 10,000 | 8 | 15–16 | about 6,276,000 |

Each envelope contained 2,048 synthetic payload characters in an OTLP JSON
request. Serialized plain WAL growth was **3,019 logical bytes/envelope** for
this particular payload/destination plan. Do not substitute the 2 KiB payload
size for journal bytes. Input preparation, filesystem setup and shutdown were
outside timing. Admission's real WriteData/Sync barriers remained enabled.
The retained files were synthetic one-byte evidence; they model metadata count
independently of disk fullness. Root scans caused 50,531–50,532 allocations per
admission with 10,000 files, versus 90 at an empty root.

The safeguards conservatively refuse admission when a metadata entry/time
budget expires, rather than approving a partial size estimate. They retain
evidence and allow replay to continue. Entry/time budget refusal is visible in
the protected management `otlpStorage.scanBudgetRejected` counter. Scans stop
as soon as a byte threshold is proven exceeded, and impossible envelopes are
rejected before scanning. Fsync barriers are preserved. Individual kernel
metadata operations and fsync can outlast the budget.

The throughput benchmark deliberately raises scan budgets to measure complete
scans. The refusal benchmark measures refused requests at 10,000 files; its
numbers are **not accepted throughput**. Run both with bounded iterations:

With a 25ms scan timeout, two repetitions of the refused-request benchmark gave:

| Entry cap | Refusal time/request | Allocation bytes/request |
|---:|---:|---:|
| 128 | 0.72–0.96ms | 107,884–109,933 |
| 1,024 | 4.46–5.16ms | about 686,700 |
| 4,096 | 19.11–19.50ms | about 2,577,600 |

These requests were refused before any WAL write/identity advancement. Tests
also verify deterministic timeout refusal, no sticky storage fault, preservation
of retained evidence and admission recovery after raising a measured budget.

```sh
go test -mod=readonly -run '^$' -bench '^BenchmarkOTLPJournal' -benchtime=256x -count=2 ./internal/controller
go test -mod=readonly -race -count=1 -shuffle=on -timeout=180s ./internal/controller ./internal/otlpstate ./internal/otlphttp ./library/otlpwire
```

The race suite passed all four packages (controller 39.187s, receipt state
9.052s, HTTP 7.457s, wire 7.649s). Existing HTTP tests need loopback listener
access; a sandbox bind refusal is an environment limitation, not a passing test.

## Usable healthy lifecycle at proposed 1024 entries / 10ms

The proposed tighter configuration was then tested with three concurrent signal
producers and the real journal scheduler/replay, receipt Sync, checkpoint and
pruning paths. Each uniquely identified original request held one real log,
metric data point or span, plus 2,048 synthetic padding characters. The local
in-process destination checked the item count and recorded exact payload bytes.
No HTTP/TLS/backend behavior is implied by this measurement. A root/WAL directory
observer sampled metadata entry counts every 2ms; highwaters are sampled values
and can miss very brief extra temporary entries. Observer work and race detection
are included in timing.

| Scenario (two repetitions) | Offered / admitted / refused | Admission completion rate | Complete lifecycle rate | Sampled entry highwater |
|---|---:|---:|---:|---:|
| Four GC/checkpoint/reopen cycles, 72 requests/cycle | 288 / 288 / 0 each | 105.89–106.36/s | 28.85–29.17/s | 49 |
| One continuous burst, 256 requests/signal | 768 / 768 / 0 each | 98.79–102.73/s | 29.84–31.67/s | 481–483 |

In those first two repetitions, all signals were accepted without starvation or
scan-budget refusal. Every
admitted request reached the peer exactly once, all accepted receipts were
reclaimed, and reopen cycles preserved the namespace. Admission completion rate
is offered concurrent burst completion, not sustainable remote delivery rate;
the burst drained afterward. Lifecycle rate includes completing durable receipts
and pruning. These data prove the chosen limits can process a normal local
healthy receipt lifecycle; they do not prove b1 sustainable throughput.

A final run with stronger zero-leftover-receipt and complete-checkpoint
assertions accepted 767/768 requests: one metric admission hit the 10ms scan
budget (0.13% refusal), with sampled entry highwater 458 and lifecycle rate
32.08/s. All 767 admitted requests were delivered and reclaimed. Thus the
selected 10ms guard is usable, but healthy storage/scheduling variation can cause
bounded telemetry dropping even below the entry cap. The measurements do not
support a zero-drop claim. If that refusal rate is unacceptable, compare the
same 1,024-entry cap with a measured 25ms time budget on b1 before widening it.

The heavier lifecycle measurement uses an explicit build tag so ordinary
repeated race workflows retain their existing runtime. The fast correctness
regressions remain in the default test suite.

```sh
go test -tags=otlp_lifecycle_measure -mod=readonly -race -run '^TestOTLPJournalChosenScanCapsHealthyLifecycle$' -v -count=2 -timeout=180s ./internal/controller
```

Keep 1,024 entries / 10ms as a provisional resource-limited edge starting point
for actual b1 qualification rather than widening the budgets from Mac results.
Monitor scan-budget refusal separately from byte fullness and destination
failure. An unresolved prefix gap or retained quarantine can grow receipts past
the entry cap and then cause every subsequent admission to be refused despite
free byte capacity. That is the intended bounded-dropping safeguard, and it
requires destination repair or a measured, reviewed budget increase. If healthy
b1 storage cannot complete normal scans within 10ms, establish the latency/CPU
cost before adjusting the timeout; a blanket larger cap could recreate the
measured per-admission allocation and CPU problem.

## Recovery capacity

Healthy direct replay to an in-process accepting peer measured **21–27
envelopes/second** over 256 admitted envelopes. This includes durable WAL copy,
receipt publication, ACK Sync and final receipt pruning. It excludes network,
TLS, Collector queues and backend persistence. Admission and replay contend
for the same WAL lock across logs, metrics and traces; eight admission clients
did not produce eight independent writers. Fsync synchronizes two WAL files
and their directory. These results do not justify removing durability barriers.

Replay to an offline peer still copies and synchronizes pending records. Its
cost scales with retained envelopes per pass, even though batches are bounded.
Healthy full batches run without an artificial pause; unresolved snapshots
retain the configured replay pause and exporter retry/not-before rules.
New admissions appear in a later frozen snapshot, so a large offline snapshot
also delays delivery of newer telemetry. Persistent 401/403 classifications can
produce retained quarantines rather than retrying after token rotation.

## Required isolated b1 qualification

First establish a safe memory/swap/CPU/I/O baseline and headroom. Run only in a
separate synthetic project/ownership directory with explicit resource limits;
never stop the production destination, fill its volume or delete its evidence.
Compare idle, healthy destination, isolated home-down, growing/full backlog and
recovery under identical offered telemetry and unpaid business probes. Record
offered/admitted/dropped envelopes per signal, payload sizes, actual WAL/root
growth, admission latency percentiles, protected management counters, destination
outcomes, process CPU/RSS, disk bytes/operations and host CPU/memory/I/O pressure.
Linux `pidstat -rud`, container statistics and `/proc/pressure/*` can supplement
request-side latency measurements. Bound experiment duration and disk size.

Measure serialized ingress `I` in bytes/second from changes in WAL logical size
while the destination is unavailable. Measure sustainable replay capacity `R`
on the real synthetic TLS/container-network/home-backend path **while new
ingress continues**. Delivery must be confirmed by exact synthetic record IDs
stored on home. Replay must have spare capacity over incoming work. For a
backlog `B`, an ideal drain estimate is `B/(R-I)` only when `R>I` and the same
units/encoding are used; actual receipt/copy/GC and remote behavior add overhead.
Replay envelope rates also depend on payload size, signal mix, retries and
destination response latency, so byte and envelope rates are both needed.

Available outage duration is at most measured available admission bytes divided
by ingress bytes/second, further reduced by metadata budgets, quarantines,
replay copies, receipts and filesystem headroom. Neither 1 GiB WAL nor 2 GiB
root thresholds promise 24 hours. Local edge admission durability and a remote
ACK do not certify power-loss durability of downstream backends or zero loss.

Promotion remains blocked until actual b1 latency/CPU/RSS/I/O and recovery
measurements meet the business gates. A lower scan budget bounds metadata work
by refusing more telemetry; it does not make all remaining edge work free.
