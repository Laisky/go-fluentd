# Superseded fixed-64-KiB candidate

Source/harness head `bba31596b73331c6fa0af7e7ba2fcc799130d241`, measured merge `39058705434957b4654df723b19c56d4c15427cc`. Hosted run: https://github.com/Laisky/go-fluentd/actions/runs/35858164130 .

All 35 workloads and 24 full-process samples are retained. The 4,096-record plain recovery scan regressed 14.7% with non-overlapping observed ranges, so the universal 64 KiB data-reader policy was superseded by adaptive read-ahead. These results are not the final candidate. Other unchanged or slower cases, including noisy whole-pipeline timing, have not been removed.
