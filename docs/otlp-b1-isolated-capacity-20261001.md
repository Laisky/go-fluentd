# Bounded b1 synthetic journal measurement

This report covers an isolated Linux amd64 run on b1 at
2026-09-30 23:59:59–2026-10-01 00:00:59 UTC. It proves the measured synthetic
admission/refusal/retry/recovery behavior under the stated bounds. It does **not**
clear the production capacity or business latency gate. Host-wide swap and
pressure activity occurred during the run, and no causal attribution is possible
from these samples. No production input, journal, network fault, paid model call,
credentials, TLS configuration or service deployment was involved.

## Reproducible scope and bounds

The opt-in `otlp_b1_measure` test uses the actual journal admission, Sync,
ReplayBatch, checkpoint and receipt-GC code. Its destination is an in-process
function that returns retryable HTTP-status 503 while offline and accepts the
exact original payload after repair. It opens no socket. Every request contains
one real OTLP log, metric point or span plus 2,048 synthetic padding characters.
Unique original payloads are checked for exactly one successful delivery.

The source base was local commit
`e4f127fbb7336ad3c4baf8b3d96f95d4eee9440f` (the previously published PR21 tree),
plus the new opt-in `internal/controller/otlp_b1_measure_test.go`:

```text
source SHA256 73731175221effa88a1046167ec2ee371bd8a1e329ea2688351aa1d8e060d7fc
Linux test binary SHA256 9086c972022f8d235eeaa31d5b9a4beea2a4e1ef49e196e32c1cb1850e214e16
```

Go 1.27.0 was crosscompiled on Mac with these existing writable caches:

```sh
GOPATH=/tmp/observability-go GOMODCACHE=/tmp/observability-go/pkg/mod \
GOCACHE=/tmp/observability-go-build GOTOOLCHAIN=auto CGO_ENABLED=0 \
GOOS=linux GOARCH=amd64 go test -tags=otlp_b1_measure,timetzdata \
  -mod=readonly -ldflags='-s -w' -c -o controller-paced-linux-amd64.test \
  ./internal/controller
```

The existing pinned local Alpine image was
`sha256:c886e22475b48ef8eb04344288ad804572baf986841b80bd6eb7ab2412cf1050`.
`timetzdata` embeds the standard Go timezone database because this image does
not provide the Asia/Shanghai database required by dependency initialization.
The container ran as host UID/GID 1000, network none, all capabilities dropped,
no-new-privileges and a read-only root. Only its dedicated synthetic state bind
mount was writable; the binary bind mount was read-only. Docker limits were
0.25 CPU, 192MiB memory and memory-swap, 64 pids, and 4MiB maximum individual file
size. Go used GOMAXPROCS=1 and GOMEMLIMIT=128MiB. The test deadline was 75s and
the container watchdog 90s, with 16MiB owned-state and 128KiB output-log stops.
The watchdog could kill only its new, previously absent container name.

The fixture intentionally scales soft byte thresholds to 128KiB WAL / 512KiB
whole root to exercise fullness in at most 108 attempted requests. It keeps the
proposed scan limits **1024 entries / 10ms**, receipt GC and replay batch 8.
These are admission thresholds, not hard filesystem quotas: replay also writes
WAL files and metadata. Paced replay temporarily reached 226,027 logical WAL
bytes, above the 128KiB admission threshold. The independently sampled owned
state highwater was 336,368 logical / 1,191,936 allocated bytes, below the
separate 16MiB watchdog. This does not test a 1GiB production WAL or metadata
accumulation at the scan cap; the separate same-cap retained-receipt regression
covers refusal, replay/GC progress, and reopen admission recovery.

## Actual b1 result

The test passed in 60.08s; its container exited 0, OOMKilled=false, with no
watchdog stop. It offered 12 healthy requests, then 96 requests at 500ms spacing
with the destination offline. Existing replay ran every four offered requests.
The full backlog was held for 10.124s while ten more existing replay batches
ran. Finally the remaining snapshot was completed, a full pending snapshot was
verified, the destination was repaired, and all admitted originals were checked.

| Phase | Result | Wall time | Admission p50 / p95 / max | Process CPU time | Linux write_bytes |
|---|---|---:|---:|---:|---:|
| Healthy admission | 12/12 admitted, four per signal | 0.0122s | 0.955 / 1.247 / 1.878ms | 0.00407s | 86,016 |
| Healthy replay | 12 delivered, checkpoint/GC 12 | 0.0922s | — | 0.01467s | 188,416 |
| Paced offline admission + replay | 39/96 admitted; 57 byte-cap refusals | 49.639s | 0.295 / 28.658 / 78.547ms | 1.40201s | 1,441,792 |
| Full offline hold | Ten batches, still above admission threshold | 10.124s | — | 0.15024s | 565,248 |
| Recovery replay | 39 delivered; checkpoint/GC 51 | 0.1414s | — | 0.03919s | 606,208 |

All 51 admitted original requests were delivered exactly once to the synthetic
peer. The 57 refused inputs were not admitted: logs 18, metrics 18, traces 21.
No request hit the scan budget. Offline accepted counts were logs 14, metrics
14, traces 11. At the end, released frontier and GC count were both 51, with
no pending backlog. The admitted payloads and final journal state were preserved.

Maximum process RSS was **62,152KiB (60.7MiB)**. Across reported phases, process
CPU user+system was 1.628171s, Linux read_bytes 942,080 and write_bytes 3,223,552.
The kernel counters describe attributed block I/O, not logical WAL bytes or
physical durability guarantees. Admission and receipt Sync barriers remained
enabled; individual fsync latency was not instrumented separately.

Healthy admission produced 38,207 logical WAL bytes for twelve mixed-signal
requests, including the fixture's journal framing. Recovery completed about
276 envelopes/s for this 39-envelope frozen backlog. These very short phases
do not establish sustained ingress or spare replay throughput under simultaneous
ingress. The 96-input paced offline phase offered about two envelopes/s and
includes retry work and deliberate sleeping. Its p95 includes both accepted
and refused requests and does not characterize production request latency.

## Host pressure and public health samples

The separate host runner sampled pressure, memory and CPU approximately once
per second and the existing unauthenticated `/api/status` route every two
seconds. It collected no response body, credentials or model traffic. Ten
baseline and six post-run probes were spaced one second apart.

| Window | Status probes | Median / max status duration | MemAvailable range | Memory / I/O / CPU PSI some avg10 maxima |
|---|---:|---:|---:|---:|
| Before | 10, all HTTP 200 | 13.076 / 19.322ms | 791,376–797,792KiB | 0 / 0.18 / 1.80 |
| During | 30, all HTTP 200 | 20.090 / 274.244ms | 734,472–793,596KiB | 0.36 / 11.20 / 12.34 |
| After | 6, all HTTP 200 | 16.428 / 279.979ms | 788,616–792,256KiB | 0 / 0.71 / 3.98 |

During the 60 host samples, cumulative interval deltas recorded **400 swap
pages in and 744 out**. Host CPU idle ranged 17.6–97.9%, with I/O wait up to
38.6%. Early spikes occurred around seconds 4–8, while additional swap-out
intervals occurred around seconds 25, 29 and 34. Last eight during-run status
probes were 11.9–17.3ms. This is host-wide evidence, with unrelated services
coexisting: the benchmark's CPU/RSS counters cannot attribute host pressure or
status delays. The post-run outlier also prevents treating the result as clear
business latency acceptance. Further capacity attribution and representative
business traffic validation remain promotion gates; this report authorizes no
additional fault test or production resource change.

## Preserved evidence and earlier attempts

All remote binaries, runner versions, logs, exited owned containers and state
remain under:

```text
/home/laisky/observability-handoff/20260930T212730Z/edge-isolated-20260930T233000Z
attempt4 container: obs-edge-isolated-20260930t233000z-4
attempt4 state: state-4/synthetic-1930675416
attempt4 log: test-4.log
attempt4 host evidence: host-evidence-4.json
```

Local copies are in the workspace `evidence/edge-performance-b1/`:
`b1-test-4.log`, `b1-host-evidence-4.json` and `local-build/`. They contain
synthetic measurement and non-secret resource data, not production input.
The source test and this report are separate from those artifacts.

Attempts 1/2 failed before exercising the journal because of Docker local-log
compression compatibility and absent timezone data, respectively. Those failed
containers/states were preserved. Attempt3 passed a 0.40s unpaced burst with
39 accepted/recovered requests, peak RSS 34,156KiB and about 192 recovery
envelopes/s; it was too short to distinguish startup noise from host pressure.
Attempt4 preserves its state independently of every earlier attempt.

Three repetitions of the Mac race dryrun passed in 13.655s using
`OTLP_MEASURE_PACED=1 OTLP_MEASURE_FAST_VALIDATE=1`; fast validation substitutes
5ms sleeps for both pace and hold and is only a harness correctness check.
Actual b1 used the normal 500ms/1000ms intervals. Default CI does not execute
this opt-in long-running measurement. No file, receipt, WAL, generation metadata
or volume was deleted as benchmark cleanup.

Real TLS/container-network ingress, backend persistence/correlation, crash
durability, representative sustained ingress/replay headroom, source privacy
and business streaming/nonstreaming/error/metric-cycle acceptance remain
outside this synthetic result. There is no 24-hour or zero-loss guarantee.
