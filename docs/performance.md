# Measured performance iterations — 2026-09-23

## Result and evidence

Six measured source iterations are complete: five targeted improvements and an adaptive-buffer correction after rejecting a reproducible regression. The gains are in decoder allocations/recovery, confirmation refresh, Fluent encoding and pending-message scraping. **There is no demonstrated end-to-end durable-throughput gain.** No writer flush, file/directory Sync, durable-acceptance response, TTL policy, or acknowledgement guarantee was weakened.

Baseline application: `d5b3e67fcf22daad1e6c667b66277bc8bbb29b7c`. Measured final source/harness head: `0e03e0b3fe32e6c0cdd19e4cb34c738ab39560fe`; CI tested merge `bf9ae9247ebfb1609cc4d0a4a2356e77f1951d5a`. Journal dependency: `v1.1.7-0.20260923121116-02361be10360` from companion `Laisky/go-journal#4`. This report and the retained dataset are documentation-only additions after those measurements.

- Final paired performance: https://github.com/Laisky/go-fluentd/actions/runs/35859450831
- Final source build/vet, coverage/race and Docker: https://github.com/Laisky/go-fluentd/actions/runs/35859450880
- Executable crash/delivery contracts: https://github.com/Laisky/go-fluentd/actions/runs/35859450942
- CodeQL: https://github.com/Laisky/go-fluentd/actions/runs/35859450881
- Journal standalone correctness: https://github.com/Laisky/go-journal/actions/runs/35858932106

All of these runs passed. No merge or deployment was performed. See [reproduction commands and workload boundaries](../tests/performance/README.md).

## Measurement discipline

Go 1.27.1, Linux amd64, GOMAXPROCS=2; hosted AMD EPYC 7763 VM with four visible vCPUs. Every final before/after pair ran on the same runner using exactly the same current harness and work units. Five counterbalanced repetitions cover all 35 component workloads; three paired repetitions cover each of four full-process profiles. Raw values, observed ranges, all slower/unchanged cases, binary hashes and environment are retained in [the final dataset](../tests/performance/results/20260923-final/).

The filesystem was ext4 on `/dev/sda1`, mounted with `nobarrier`, `data=writeback` and `journal_async_commit`. These are comparative runner measurements, **not production SSD capacity or physical power-loss qualification**. Sync calls remain present, but a CI storage stack must not be treated as a validated production durability platform. Reads are cache-hot, and the usual payload is about 2 KiB of highly compressible text plus metadata. Instrumented race tests and profiling runs are separate from timing samples.

## Iteration decisions

| Iteration | Committed change | Quantitative decision |
|---|---|---|
| 1 | `d61f56b` (journal): smaller decoder buffers | Local allocation profile attributed 93.95% of allocated bytes in a small recovery probe to the two 4 MiB reader buffers. This justified testing bounded lookahead, not changing writer buffers. |
| 2 | `9201e2b` (journal): one atomic Swap for refreshed confirmations | Six to two allocations per refresh. Existing generation locking, unique cardinality and non-consuming lookups are retained. |
| 3 | `8e01d4a` (application): direct Fluent Forward framing | Typed framing eliminates temporary wrappers, slice boxing and their steady-state allocations. Independent wire and failure tests pass. |
| 4 | `f1fae29` (journal): fixed-width ID scratch reuse | Four to three allocations per unique ID write. Small timing changes were inconsistent; this is an allocation reduction, not a claimed ID-write throughput win. |
| 5 | `062e8d2` (application): event-maintained pending gauge | The pending-message count changes only when a partially acknowledged message enters or leaves the existing map. No periodic scan of all pending records is needed. |
| 6 | `052e2a2` (journal): adaptive plain-file read-ahead | Reject the universal 64 KiB policy after a 14.7% large plain-scan regression. Keep 4 MiB read-ahead for uncompressed regular files at least 4 MiB long; keep other readers at 64 KiB. |

The rejected fixed-size campaign is [retained in full](../tests/performance/results/20260923-fixed64k/): large plain recovery was 6.921 ms baseline versus 7.937 ms candidate with non-overlapping observed ranges. In the corrected campaign it is 7.078 ms baseline versus 5.568 ms candidate. This comparison uses each campaign's own paired baseline; values from different runners are not mixed into a speedup calculation.

## Complete final component measurements

Negative change means less time per operation. Recovery/replay rows describe a whole scan; encoder rows describe a batch. `components.jsonl` retains the explicit work-unit denominator and normalized rate. Monitor operations are scrapes, not messages. Allocated bytes per operation are not whole-process RSS.

| Workload | Before ns/op | After ns/op | Change | B/op before → after | allocs/op before → after |
|---|---:|---:|---:|---:|---:|
| `BenchmarkPerfAcceptorFilter` | 2,235.0 | 2,217.0 | -0.8% | 1,389 → 1,388 | 20 → 20 |
| `BenchmarkPerfConcatenator` | 3,012.0 | 3,017.0 | +0.2% | 1,034 → 1,035 | 19 → 19 |
| `BenchmarkPerfDispatcher` | 510.2 | 520.0 | +1.9% | 0 → 0 | 0 → 0 |
| `BenchmarkPerfFluentEncoder/batch=1` | 944.2 | 616.6 | -34.7% | 112 → 0 | 5 → 0 |
| `BenchmarkPerfFluentEncoder/batch=512` | 237,495.0 | 148,887.0 | -37.3% | 24,880 → 0 | 1029 → 0 |
| `BenchmarkPerfFluentEncoder/batch=64` | 29,167.0 | 18,000.0 | -38.3% | 3,141 → 0 | 131 → 0 |
| `BenchmarkPerfHTTPReceive` | 10,438.0 | 10,406.0 | -0.3% | 14,387 → 14,387 | 59 → 59 |
| `BenchmarkPerfHTTPSender/es` | 1,842,583.0 | 1,826,646.0 | -0.9% | 914,117 → 911,850 | 4687 → 4686 |
| `BenchmarkPerfHTTPSender/http` | 1,602,232.0 | 1,579,765.0 | -1.4% | 899,482 → 858,709 | 2847 → 2843 |
| `BenchmarkPerfJournalAppend/gzip=false/syncEvery=0` | 4,532.0 | 4,207.0 | -7.2% | 0 → 0 | 0 → 0 |
| `BenchmarkPerfJournalAppend/gzip=false/syncEvery=1` | 324,399.0 | 326,533.0 | +0.7% | 200 → 200 | 3 → 3 |
| `BenchmarkPerfJournalAppend/gzip=false/syncEvery=64` | 10,022.0 | 9,719.0 | -3.0% | 3 → 3 | 0 → 0 |
| `BenchmarkPerfJournalAppend/gzip=true/syncEvery=0` | 9,682.0 | 9,752.0 | +0.7% | 397 → 397 | 0 → 0 |
| `BenchmarkPerfJournalAppend/gzip=true/syncEvery=1` | 415,818.0 | 409,732.0 | -1.5% | 597 → 597 | 3 → 3 |
| `BenchmarkPerfJournalAppend/gzip=true/syncEvery=64` | 17,413.0 | 17,267.0 | -0.8% | 400 → 400 | 0 → 0 |
| `BenchmarkPerfJournalIDs/gzip=false` | 2,001.0 | 2,017.0 | +0.8% | 129 → 121 | 4 → 3 |
| `BenchmarkPerfJournalIDs/gzip=true` | 855.0 | 887.3 | +3.8% | 527 → 518 | 4 → 3 |
| `BenchmarkPerfJournalRecovery/gzip=false/records=4096` | 7,077,700.0 | 5,568,315.0 | -21.3% | 19,091,284 → 14,924,406 | 96283 → 94233 |
| `BenchmarkPerfJournalRecovery/gzip=false/records=64` | 597,345.0 | 184,583.0 | -69.1% | 8,568,290 → 303,132 | 1528 → 1495 |
| `BenchmarkPerfJournalRecovery/gzip=true/records=4096` | 26,910,837.0 | 26,357,506.0 | -2.1% | 19,254,324 → 10,974,476 | 96297 → 94247 |
| `BenchmarkPerfJournalRecovery/gzip=true/records=64` | 1,033,437.0 | 527,686.0 | -48.9% | 8,744,044 → 474,999 | 1544 → 1509 |
| `BenchmarkPerfJournalReplay/gzip=false` | 3,141,535.0 | 2,256,292.0 | -28.2% | 11,002,314 → 2,735,332 | 26236 → 25720 |
| `BenchmarkPerfJournalReplay/gzip=true` | 8,711,222.0 | 7,404,195.0 | -15.0% | 11,104,269 → 2,822,272 | 26250 → 25733 |
| `BenchmarkPerfJournalTTL/hit` | 59.2 | 59.2 | +0.1% | 0 → 0 | 0 → 0 |
| `BenchmarkPerfJournalTTL/miss` | 66.2 | 66.9 | +1.0% | 0 → 0 | 0 → 0 |
| `BenchmarkPerfJournalTTL/refresh` | 235.7 | 135.4 | -42.6% | 95 → 63 | 6 → 2 |
| `BenchmarkPerfMonitor` | 50,397.0 | 51,622.0 | +2.4% | 42,128 → 42,123 | 489 → 489 |
| `BenchmarkPerfParser` | 3,473.0 | 3,480.0 | +0.2% | 1,864 → 1,864 | 22 → 22 |
| `BenchmarkPerfPendingMonitor/pending=0` | 50,483.0 | 50,055.0 | -0.8% | 40,407 → 40,406 | 476 → 476 |
| `BenchmarkPerfPendingMonitor/pending=1000` | 78,213.0 | 64,736.0 | -17.2% | 42,175 → 42,162 | 492 → 492 |
| `BenchmarkPerfPendingMonitor/pending=100000` | 2,797,437.0 | 68,320.0 | -97.6% | 42,217 → 42,108 | 492 → 492 |
| `BenchmarkPerfPostFilter/rename=false` | 1,063.0 | 1,028.0 | -3.3% | 831 → 831 | 7 → 7 |
| `BenchmarkPerfPostFilter/rename=true` | 2,110.0 | 2,068.0 | -2.0% | 1,863 → 1,863 | 17 → 17 |
| `BenchmarkPerfProducer/sinks=1` | 1,467.0 | 1,484.0 | +1.2% | 18 → 18 | 2 → 2 |
| `BenchmarkPerfProducer/sinks=2` | 2,443.0 | 2,444.0 | +0.0% | 66 → 66 | 3 → 3 |

## Durable full-process results

Each profile has 1,024 timed accepted events plus one excluded warmup event, two independently fsynced sinks, batches of 64, and per-record durable HTTP acceptance. Both persisted sink ledgers were reread and reconciled against the producer manifest. All **24,576 timed accepted events produced 49,152 expected sink deliveries**, with no missing, extra or duplicate event. Latency values below are medians of the three per-run P99 values, not an open-loop SLO.

| Profile | Before delivered msg/s | After delivered msg/s | Change | Before P99 ms | After P99 ms |
|---|---:|---:|---:|---:|---:|
| `gzip=False,concurrency=1` | 1,265.0 | 1,249.5 | -1.2% | 2.970 | 3.052 |
| `gzip=False,concurrency=16` | 1,837.3 | 1,812.8 | -1.3% | 14.031 | 13.904 |
| `gzip=True,concurrency=1` | 1,192.5 | 1,168.7 | -2.0% | 2.905 | 3.167 |
| `gzip=True,concurrency=16` | 1,770.4 | 1,742.8 | -1.6% | 14.466 | 14.437 |

The final medians are 1.2–2.0% lower, so no end-to-end speedup is claimed. The earlier rejected campaign also had lower pipeline medians. Raw ranges, per-profile CPU seconds, peak RSS and latency samples remain visible; the final gzip/concurrency-16 profile has a slightly lower candidate throughput range, not evidence to hide. These small differences do not establish a causal production regression, but neither do they support a claim of higher throughput.

The remaining per-record durable-write cost is visible directly: plain append plus Sync is 324.4 versus 326.5 microseconds per record; gzip is 415.8 versus 409.7 microseconds. Neither is a meaningful improvement. Final-only Sync and per-64-record Sync are separate diagnostic workloads, not interchangeable guarantees. The ES output used in this pipeline does not execute the Fluent wire encoder, so an encoder microbenchmark improvement cannot be promoted to a pipeline speedup.

## Correctness and next measured bottleneck

New controls validate large plain/gzip records, byte-exact acknowledgement encoding, truncated ID errors, concurrent refresh/cardinality, exact Fluent frames and real all-sender pending transitions. Existing build/vet, coverage floors, five shuffled full-suite race runs, ten repeated component/regression race runs, two seeds of 21 full-process delivery contracts, Docker smoke/test images, and journal recovery tests remain enabled. Temporary source-publication workflows are removed from the final tree; normal performance CI is read-only and retains all samples rather than enforcing noisy timing thresholds.

No production Kafka/Elasticsearch cluster, cold storage device, arbitrary payload distribution, unbounded backlog, or power-loss guarantee is inferred from this suite. The protocol-peer/client cost is included in the closed-loop pipeline measurement.

The next substantial throughput target is a separately specified group-commit design: preserve success-after-Sync while bounding the wait, propagating shared Sync errors and validating crash cut points. That design is not implemented in this change. Removing Sync or comparing a weaker acknowledgement policy would not be an acceptable optimization.
