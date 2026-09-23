# Paired performance results

Medians of same-host alternating runs. Raw samples/ranges are retained; timing changes are not significance claims.

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
