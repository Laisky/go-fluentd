# Paired performance results

Medians of same-host alternating runs. Raw samples/ranges are retained; timing changes are not significance claims.

| Workload | Before ns/op | After ns/op | Change | B/op before → after | allocs/op before → after |
|---|---:|---:|---:|---:|---:|
| `BenchmarkPerfAcceptorFilter` | 2,219.0 | 2,176.0 | -1.9% | 1,388 → 1,388 | 20 → 20 |
| `BenchmarkPerfConcatenator` | 2,925.0 | 2,953.0 | +1.0% | 1,035 → 1,034 | 19 → 19 |
| `BenchmarkPerfDispatcher` | 507.7 | 506.3 | -0.3% | 0 → 0 | 0 → 0 |
| `BenchmarkPerfFluentEncoder/batch=1` | 936.1 | 616.9 | -34.1% | 112 → 0 | 5 → 0 |
| `BenchmarkPerfFluentEncoder/batch=512` | 241,306.0 | 148,013.0 | -38.7% | 24,917 → 0 | 1029 → 0 |
| `BenchmarkPerfFluentEncoder/batch=64` | 29,426.0 | 17,910.0 | -39.1% | 3,140 → 0 | 131 → 0 |
| `BenchmarkPerfHTTPReceive` | 10,542.0 | 10,848.0 | +2.9% | 14,388 → 14,387 | 59 → 59 |
| `BenchmarkPerfHTTPSender/es` | 1,831,957.0 | 1,857,840.0 | +1.4% | 913,336 → 913,344 | 4686 → 4687 |
| `BenchmarkPerfHTTPSender/http` | 1,547,407.0 | 1,619,507.0 | +4.7% | 862,840 → 919,680 | 2844 → 2848 |
| `BenchmarkPerfJournalAppend/gzip=false/syncEvery=0` | 4,049.0 | 4,208.0 | +3.9% | 0 → 0 | 0 → 0 |
| `BenchmarkPerfJournalAppend/gzip=false/syncEvery=1` | 351,124.0 | 343,765.0 | -2.1% | 200 → 200 | 3 → 3 |
| `BenchmarkPerfJournalAppend/gzip=false/syncEvery=64` | 10,757.0 | 9,902.0 | -7.9% | 3 → 3 | 0 → 0 |
| `BenchmarkPerfJournalAppend/gzip=true/syncEvery=0` | 9,678.0 | 9,694.0 | +0.2% | 397 → 397 | 0 → 0 |
| `BenchmarkPerfJournalAppend/gzip=true/syncEvery=1` | 479,359.0 | 473,986.0 | -1.1% | 597 → 597 | 3 → 3 |
| `BenchmarkPerfJournalAppend/gzip=true/syncEvery=64` | 17,605.0 | 19,684.0 | +11.8% | 400 → 400 | 0 → 0 |
| `BenchmarkPerfJournalIDs/gzip=false` | 1,969.0 | 1,888.0 | -4.1% | 129 → 121 | 4 → 3 |
| `BenchmarkPerfJournalIDs/gzip=true` | 827.2 | 776.4 | -6.1% | 525 → 519 | 4 → 3 |
| `BenchmarkPerfJournalRecovery/gzip=false/records=4096` | 6,921,020.0 | 7,937,009.0 | +14.7% | 19,095,440 → 10,821,382 | 96284 → 94234 |
| `BenchmarkPerfJournalRecovery/gzip=false/records=64` | 588,522.0 | 173,881.0 | -70.5% | 8,568,195 → 302,944 | 1528 → 1494 |
| `BenchmarkPerfJournalRecovery/gzip=true/records=4096` | 26,675,385.0 | 24,575,490.0 | -7.9% | 19,254,038 → 10,974,553 | 96297 → 94247 |
| `BenchmarkPerfJournalRecovery/gzip=true/records=64` | 996,932.0 | 535,771.0 | -46.3% | 8,740,347 → 475,048 | 1543 → 1509 |
| `BenchmarkPerfJournalReplay/gzip=false` | 3,068,409.0 | 2,241,289.0 | -27.0% | 11,000,865 → 2,740,376 | 26236 → 25720 |
| `BenchmarkPerfJournalReplay/gzip=true` | 8,837,983.0 | 7,099,291.0 | -19.7% | 11,109,751 → 2,818,740 | 26251 → 25733 |
| `BenchmarkPerfJournalTTL/hit` | 59.9 | 58.7 | -2.0% | 0 → 0 | 0 → 0 |
| `BenchmarkPerfJournalTTL/miss` | 66.9 | 66.0 | -1.4% | 0 → 0 | 0 → 0 |
| `BenchmarkPerfJournalTTL/refresh` | 238.9 | 126.7 | -47.0% | 95 → 63 | 6 → 2 |
| `BenchmarkPerfMonitor` | 51,241.0 | 50,848.0 | -0.8% | 42,124 → 42,120 | 489 → 489 |
| `BenchmarkPerfParser` | 3,252.0 | 3,329.0 | +2.4% | 1,864 → 1,864 | 22 → 22 |
| `BenchmarkPerfPendingMonitor/pending=0` | 48,417.0 | 48,884.0 | +1.0% | 40,409 → 40,404 | 476 → 476 |
| `BenchmarkPerfPendingMonitor/pending=1000` | 74,908.0 | 65,661.0 | -12.3% | 42,161 → 42,169 | 492 → 492 |
| `BenchmarkPerfPendingMonitor/pending=100000` | 2,878,869.0 | 65,665.0 | -97.7% | 42,221 → 42,107 | 492 → 492 |
| `BenchmarkPerfPostFilter/rename=false` | 1,055.0 | 1,019.0 | -3.4% | 831 → 831 | 7 → 7 |
| `BenchmarkPerfPostFilter/rename=true` | 2,054.0 | 2,074.0 | +1.0% | 1,863 → 1,863 | 17 → 17 |
| `BenchmarkPerfProducer/sinks=1` | 1,442.0 | 1,431.0 | -0.8% | 18 → 18 | 2 → 2 |
| `BenchmarkPerfProducer/sinks=2` | 2,448.0 | 2,377.0 | -2.9% | 66 → 66 | 3 → 3 |
