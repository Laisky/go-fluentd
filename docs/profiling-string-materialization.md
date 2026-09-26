# Profile-guided JSON string materialization

## Baseline and bounded scope

This increment continues PR18 on `perf/e2e-load-20260925` from
`a2a5b686edc06afed01e923a2257afa6f919cfb3`, not the older master baseline.
It changes only the event JSON tokenizer's string materialization in production.
The existing driver, configuration, durability barriers, dependencies, README
architecture diagram and historical measurement rows remain unchanged.

The tokenizer reads a validated JSON string with `jsontext.Decoder.ReadValue`.
When it contains no backslash, conversion of its interior bytes to a string
creates caller-owned storage without an intermediate unquote buffer. Escaped
strings still use standard-library `jsontext.AppendUnquote`. No unsafe string
alias is introduced. Syntax, UTF-8, surrogate, duplicate decoded key, nesting
and integer-domain checks still run. This is an allocation optimization, not a
claim that the prior decoder returned incorrect values.

References: [jsontext API](https://pkg.go.dev/encoding/json/jsontext),
[prior sustained method](sustained-profiling.md).

## Profile evidence and rejected experiment

Linux amd64, Go 1.27.1, native committed modules, GOMAXPROCS=4, a four-core cgroup
quota and 4 GiB memory limit. The shared host exposes five logical CPUs on an AMD
EPYC 9V74; utilization is normalized to the effective four-core quota. Application
and driver CPU are separate. Journals are real local files on overlay storage;
HTTP mock acknowledgements do not simulate a real backend's fsync.

Fresh baseline and candidate diagnostics capture a 15-second CPU profile between
two heap snapshots, starting five seconds after warmup. No forced GC is requested.
Their whole-run qualification uses the unchanged gate: trim three seconds at each
end; at least 15 one-second windows; mean application CPU >=50%; >=80% of windows
above 50%; delivered-rate coefficient of variation <=0.20; at most 64 outstanding
requests and correct delivery. Baseline diagnostic: 200,000 requests; candidate:
300,000, to keep the sampling window away from the tail. Those different-sized
**diagnostic** trials are not paired throughput evidence.

| Clean diagnostic | Steady seconds | Mean quota utilization | Windows >=50% | Rate CV |
|---|---:|---:|---:|---:|
| Baseline | 17 | 58.30% | 100.00% | 0.157 |
| Candidate | 30 | 57.34% | 96.67% | 0.161 |

Both qualify. The baseline attributed 30.34% of sampled allocation to
`jsontext.Token.String` including its temporary unquote growth. The candidate
removes that temporary allocation on unescaped Unicode strings; the required
owned string allocation remains. Profiles are diagnostic samples, not precise
allocation totals or RSS measurements. Subsequent allocation leaders are msgp
`ReadString`, output-buffer growth, owned token strings, request-body storage and
`io.copyBuffer`. CPU remains distributed across syscall, JSON quoting and GC.

A direct JSON output rewrite was evaluated first and **rejected**. For the same
16 KiB private-buffer output fixture, the existing encoder used about 18.3-19.0
microseconds, 18.7 KB and 11 allocations; the tested direct streaming alternative
used about 29.6-33.0 microseconds, 78.4 KB and 18 allocations. Output equality passed,
but no output-path production change was retained. This is not a general claim
about every JSON v2 use case.

An initial candidate profile's full run overlapped later race compilation. It is
retained as contaminated diagnostic evidence and excluded from clean qualification
and paired comparisons. A separate clean candidate profile and all six timed
trials below ran without concurrent compilation, tests or profiling.

## Fixed unprofiled A/B campaign

Three serial **AB / BA / AB** pairs use 300,000 NDJSON requests per trial,
16 KiB text containing Unicode, end-to-end concurrency 64, one required mock,
plain journal, per-record synchronization, batch/group size one and 64 untimed
warmup requests. `--delivery-window` releases a slot only when all destinations
validate the payload; `--bounded-fixtures` avoids retaining every large body.
Both binaries use the same driver and settings, including explicit 10 ms OTLP
replay. No profiling is enabled in these six trials.

All **1,800,000 measured requests** were accepted and accounted for at the
required destination, with no missing records, request errors or generator drops.
All six processes exited gracefully with status zero. The ledgers record **40
identical duplicate observations**: one in candidate pair 1 and 39 in baseline
pair 2 (zero-based pair numbers). The existing at-least-once contract allows
identical retries; neither side is claimed to provide exactly-once delivery.

| Metric, median | Baseline | Candidate | Ratio of medians |
|---|---:|---:|---:|
| Delivered records/s | 7,628.07 | 8,119.87 | +6.45% |
| Application CPU microseconds/record | 310.43 | 285.70 | -7.97% |
| Destination P99 ms | 19.855 | 18.322 | -7.72% |
| Admission P99 ms | 6.268 | 5.989 | -4.46% |
| Peak RSS MiB | 198.79 | 199.15 | +0.18% |
| Cumulative allocated bytes/record | 117,937 | 99,209 | -15.88% |

All three pairs improve throughput (+5.32% to +7.36%), CPU/record (-7.89% to
-9.54%), allocation and both latency P99s. **There is no repeatable RSS reduction:**
individual RSS changes are +3.38%, +2.02% and -2.27%. Lower allocation churn is
not lower live heap or resident memory. Three pairs on one shared machine are
not a statistical confidence claim, production SLO, or sustained open-loop
capacity certification. Do not combine these numbers with earlier hosts or
baseline revisions. The earlier long-horizon capacity limits remain applicable.

### Every timed trial passed the original steady-load gate

| Pair / version | Seconds | Mean CPU | Windows >=50% | Rate CV | Max outstanding |
|---|---:|---:|---:|---:|---:|
| 0 / baseline | 32 | 59.64% | 100.00% | 0.151 | 64 |
| 0 / candidate | 30 | 57.96% | 100.00% | 0.149 | 64 |
| 1 / baseline | 33 | 59.73% | 100.00% | 0.140 | 64 |
| 1 / candidate | 30 | 58.00% | 100.00% | 0.154 | 64 |
| 2 / baseline | 33 | 59.23% | 96.97% | 0.148 | 64 |
| 2 / candidate | 31 | 57.40% | 96.77% | 0.182 | 64 |

No gate was relaxed or unsuccessful timed sample replaced. Full-precision rows
are committed in [the CSV](../tests/loadtest/results/20260925-string-materialization.csv).
The existing comparison runner independently audited all six saved trials.

## Mechanism and correctness checks

Three local microbenchmark repetitions compare the frozen previous tokenizer
against the candidate using identical inputs. A 16 KiB Unicode string goes from
37,672 to 19,240 bytes/op, 16 to 15 allocations, and median 42.139 to 21.873
microseconds. Dense Unicode goes from 35,624 to 17,192 bytes/op. Escaped text
retains 34,856 bytes/op and 16 allocations; timings are effectively unchanged.
These benchmarks explain the mechanism, not whole-application throughput.

Native Go 1.27.1 acceptance on this implementation:

- Full repository suite: **1,104 test/subtest passes**, no failures or skips.
- Three shuffled full-suite race repetitions: **3,312 passes**, no race reports,
  failures or skips. Five earlier focused package race repetitions also passed.
- New frozen-tokenizer differential fuzz: **248,119 executions**, passed. Existing
  reader-based parity tests and malformed-input seeds remain enabled.
- Module verification, complete build/vet and formatting passed. No custom modfile
  or replacement module is used.
- Actual race-instrumented configured application: both plain/gzip mixed-pipeline
  crash cases passed, 40 items accounted for, two SIGKILL/reopens, new progress
  and graceful exits. This is separate from the performance campaign.

New tests overwrite the input buffer after parsing and verify independent Unicode
keys/values; 24 concurrent workers exercise ownership. Raw/escaped equivalents,
invalid UTF-8, lone surrogates, duplicate decoded keys and truncated/extra values
are checked. The old decoder is a differential performance reference, not an
unsafe implementation that should fail these behavioral assertions.

## Reproduce and continue

Use the unchanged [comparison runner](../tests/loadtest/compare.py) and the new
[fixed case](../tests/loadtest/cases/string-materialization.json). On a Linux host
with the pinned native dependencies and Go toolchain available:

```sh
go build -mod=readonly -o /tmp/loadtest ./tests/loadtest
go build -mod=readonly -o /tmp/candidate .
git worktree add --detach /tmp/pr18-string-baseline a2a5b686edc06afed01e923a2257afa6f919cfb3
(cd /tmp/pr18-string-baseline && go build -mod=readonly -o /tmp/baseline .)
GOMAXPROCS=4 python3 tests/loadtest/compare.py --driver /tmp/loadtest \
  --baseline /tmp/baseline --candidate /tmp/candidate --pairs 3 \
  --cases tests/loadtest/cases/string-materialization.json --out /tmp/string-pairs
for trial in /tmp/string-pairs/ndjson-*; do
  [ -d "$trial" ] || continue
  python3 tests/loadtest/sustained.py "$trial"
done
```

The existing hosted load workflow compares with the older master baseline and
uses smaller protocol profiles. It does not independently reproduce this full
1.8-million-request campaign. Final-head CI status belongs in the PR, not a
forward claim that an as-yet-unrun workflow passed.

This bounded increment finishes one measured hotspot after rejecting another.
Remaining profile targets include MessagePack string allocation and HTTP output
copy buffers. They require new ownership/retry tests and isolated E2E evidence,
not unsafe aliases or removal of synchronization. No receipt compaction, global
quota, runtime-default changes or additional protocol performance claim is added.
