# Output-path investigation: no production change adopted

## Identity and decision

Baseline: PR18 `0d704e255d2f2e3e095eebb24c0959db06de20f4`,
source tree `861850a9b89b518d8f4a0e4e831ce129b81e80d6`.
The application driver and committed dependency graph are unchanged.
This continuation recovers previously unapplied receiver tests and reconciles
archived work; it does **not** claim a new production speedup.

A qualified baseline profile identified MessagePack replay strings and private
JSON output as major allocators. Three bounded output-path experiments were
then tested. None supplies enough evidence to adopt another production change.
Their source, measurements and rejected or unqualified outcomes are retained.

## Qualified baseline diagnostic

Native Go 1.27.1, Linux amd64, shared AMD EPYC 9V74 host, four-core cgroup quota,
4 GiB memory limit, GOMAXPROCS=4 and overlay filesystem. The driver and application
share the environment but their CPU counters are separate.

The ordinary configured binary processed 500,000 NDJSON requests with 16 KiB
Unicode text, end-to-end concurrency 128, bounded fixtures, a real
per-record-synchronized plain journal and one independent local HTTP mock.
There were no missing records, request errors, generator drops or duplicates.
This is a diagnostic campaign, not a timed capacity comparison.

After trimming three seconds from either end, all **45** one-second windows
exceeded 50% of effective CPU capacity. Mean application utilization was
**56.93%**, delivered-rate CV **0.1562**, and maximum outstanding delivery work
**128**. The unchanged qualifier passed. CPU capture lasted 15 seconds after a
five-second delay, with two heap snapshots and no forced GC.

Sampled allocation difference:

| Flat allocation site | MiB | Share |
|---|---:|---:|
| `msgp.(*Reader).ReadString` | 3,085.90 | 30.38% |
| `bufferTokens.Token` | 2,681.41 | 26.40% |
| `bytes.growSlice` | 2,639.36 | 25.99% |

Allocation sampling is not RSS, and these percentages are not total CPU shares.
The CPU sample includes JSON quote/scan work, GC and system calls.

## Experiments and why they were not adopted

### Primitive JSON output writer

A restricted primitive-map writer preserved the tested fixture's exact output,
but its 16 KiB benchmark took **35.1-36.0 microseconds** instead of the current
**22.6-23.6 microseconds**. Fewer allocation events did not make it faster.
The implementation also did not establish the complete domain contract for
arbitrary supported values. It was rejected before application-level testing.

### Hiding the connection's optional copy interface

A diagnostic connection wrapper was compared with the existing transport at
512 B, 16 KiB, 64 KiB and 128 KiB. It produced no repeatable allocation benefit.
It was not introduced into the production transport. Existing TLS, retry and
connection ownership behavior remain untouched.

### Reuse encoder state, not returned body bytes

A separate prototype pooled the encoder and buffer descriptor, detaching the
output allocation before releasing the descriptor. Returned body bytes stayed
private; the output was checked after later encodes and an intervening encoding
error. This is **not** the previously rejected reference-counted outgoing-body
pool, and it does not borrow transport-owned body storage.

A 16 KiB microbenchmark reduced approximately 18,704 to 18,545 bytes/op and
11 to 9 allocations. Timings were essentially unchanged. Small 512 B fixtures
showed a modest local improvement, not proof of an application-wide benefit.

One complete **AB** application screening pair used the same frozen driver and
settings for both binaries: 500,000 measured requests per trial, 16 KiB NDJSON,
end-to-end concurrency 128, profiling disabled. No concurrent compilation,
tests or other load experiments were run. Shared-host/background runtime
variability was not controlled; the stability gate is therefore material.

| Screening metric | Baseline | Prototype |
|---|---:|---:|
| Delivered records/s | 7,964.94 | 9,651.67 |
| App CPU microseconds/record | 252.04 | 235.10 |
| Destination P99 ms | 29.167 | 27.866 |
| Admission P99 ms | 7.226 | 6.184 |
| Peak RSS MiB | 271.273 | 252.812 |
| Allocated bytes/record | 67,207.39 | 64,976.44 |
| Mean app CPU / four-core capacity | 49.991% | 56.767% |
| Windows at or above 50% CPU | 66.07% | 100% |
| Delivered-rate CV | 0.3456 | 0.1633 |
| Complete stability qualification | **Failed** | Passed |

Both trials accepted and accounted for all 500,000 measured requests, without
duplicates, errors, drops or missing records, and exited gracefully. Each also
has 64 untimed warmup requests. Both original saved-trial audits passed.

**The apparent 21.18% throughput improvement is not an accepted optimization
result.** The baseline failed the original CPU occupancy and variability gates;
only one screening pair ran. It was not rerun away, replaced, included in earlier
paired medians or used to justify adoption. The prototype is archived, not
committed as production code. The later production candidate remains identical
to the baseline because only tests/documentation are being published.

[Both full-precision screening rows](../tests/loadtest/results/20260926-output-probes.csv)
retain the successful delivery result and failed stability result separately.
The raw request/resource records and prototype source and patches are retained
in the downloadable continuation evidence. Routine hosted CI does not reproduce
this screening experiment or establish its performance validity.

## MessagePack finding and next bounded design

The sampled `ReadString` path belongs to `go-journal`'s legacy reader. In the
exact locked dependency (`v1.1.7-0.20260924003054-612f5354f538`), `LegacyLoader.Load`
fully decodes a record and only then checks/removes its committed ID.
The generated encoding writes `Data` before `ID`. Consequently, many already
acknowledged large strings are materialized while sealed segments are scanned.
This is an observed cost, not by itself a corruption or delivery defect.

Do not replace those owned strings with borrowed decoder buffers: pending records
leave the iterator and must remain valid across later reads, queues and retries.
Do not skip a segment based only on the largest ACK, because ACKs can be sparse
or out of order. Do not skip malformed/truncated data in the name of throughput.

A useful next journal-specific prototype needs a validated raw-record scan or
a backwards-compatible selective decode API, preserving the original bytes until
the ID/ACK decision is known. It must handle existing Data-before-ID files,
unknown fields, sparse ACKs, partial writes, compression, reader reuse and
cleanup ordering. Compare mixed committed/uncommitted segments, not only an
all-acknowledged best case. Only then bind it to the application and repeat
unprofiled matched E2E trials. No dependency replacement or new journal format
is included in this continuation.

## Recovered tests and fresh verification

The archived supplemental receiver test is now part of the source tree, rather
than an unapplied attachment. It exercises five formats, 72 size/hint/limit cases,
prefix rejection after read/parse failures, 24 concurrent workers and bounded
differential fuzz. The private-reader reference is frozen from `0dc085cf`.

Fresh native checks on the adopted production source plus those recovered tests:

- Complete suite: **1,215 test/subtest passes**, no failed or skipped tests.
- Three shuffled full-suite race repetitions: **3,645 passes**, no race reports.
- Focused recovered tests: **243 passes** across three shuffled race repetitions.
- Differential receiver fuzz: **20,064 executions**, passed.
- Ignoring a late error after a valid body causes the recovered prefix assertion
  to fail. Its independent ownership control and restored implementation pass.
- Native module verification and vet passed with unchanged dependencies.

These results are not copied from an earlier PR description. Packages that have
no test files are not counted as executed tests. No unsafe mutation remains in
the published source. See [workspace reconciliation](pr18-workspace-reconciliation.md)
for the exact disposition of each archived candidate and the single active branch.

This is one measured investigation with rejected/unqualified candidates, not
proof that further optimization is impossible. Historical gains, limitations and
failed capacity tests in the previous reports remain unchanged.
