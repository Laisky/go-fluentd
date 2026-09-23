# Durable group commit: measured result and conservative rollout

Measured source/harness head: `cdf1129e5a8cd3a270304f0a11ca0298ce3d0c39`; tested merge: `3f1091a64192d0ec751fa0b8f4da326d1b5dd7d3`. This report and its dataset are documentation-only additions. Baseline executable: merged #11, `76717b288a170acc0e1e12daf19def9fa7da1bb1`; the current master tree at `28f221d` is identical. Journal service baseline `cf9833b` extracts the same original per-record writer. No dependency version or on-disk format changes are needed.

## Result

Ready-only grouping materially reduces amortized journal service cost under concurrent requests. The full-process concurrent medians also improve, but tail latency and low-load observations are not uniformly better. **Grouping is opt-in, not a new default.** `group_commit_max_messages: 0` or `1` preserves per-record Sync; `64` enables grouping, with a configurable upper bound of 1024. There is no timer waiting for a second message.

Every reliable receipt still follows all writes and a successful shared Sync. A failed write or barrier fails the affected owned group; a failure/timeout is an unknown outcome, not proof that no bytes were written. Cancellation cannot abort a kernel Sync already in progress and cannot replace its actual completed result. Size bounds do not impose a wall-clock bound on storage I/O.

## Measurement discipline

Six counterbalanced before/after pairs per profile, Go 1.27.1, Linux amd64 and GOMAXPROCS=2. Each pair uses the same runner and identical current harness. Profiles run on separate hosted VMs: do not interpret cross-profile values as a scaling curve. Exact CPU, filesystem, revisions and binary hashes are in the dataset. The service benchmark uses 2048 two-KiB records per sample; the full executable uses 8192 timed records plus an excluded warmup, durable HTTP acceptance and two independently fsynced Elasticsearch-protocol peers. The benchmark configures before=1 and after=64 explicitly.

Timing binaries are uninstrumented; race runs and evidence auditing are separate. All values below are medians; raw samples, min/max and paired ratios are retained, with no statistical-significance claim. P99 is HTTP durable-acceptance latency, **not end-to-end delivery latency**. The closed-loop client, protocol peers, disk writes and finite-batch completion contribute to pipeline timings. Highly compressible payloads and ext4 CI volumes with `nobarrier`, `data=writeback`, and `journal_async_commit` are not production-device capacity or physical-power-loss qualification.

## Journal service

| Profile | Before microseconds/message | Grouped | Time change | Grouped Sync calls/message |
|---|---:|---:|---:|---:|
| gzip=false/clients=1 | 351.716 | 352.522 | +0.23% | 1.00000 |
| gzip=false/clients=16 | 362.476 | 52.389 | -85.55% | 0.12475 |
| gzip=false/clients=64 | 353.945 | 17.597 | -95.03% | 0.03125 |
| gzip=true/clients=1 | 410.834 | 421.459 | +2.59% | 1.00000 |
| gzip=true/clients=16 | 419.885 | 62.644 | -85.08% | 0.12450 |
| gzip=true/clients=64 | 425.311 | 23.642 | -94.44% | 0.03125 |

Service time is amortized completed-record cost, not one request's latency. The baseline uses exactly one Sync call per record. Fewer calls cover multiple records behind the same success-after-Sync barrier; this is not a comparison with buffered-only acknowledgement. Single-client plain/gzip service cost is slightly worse and is retained.

## Full executable and two sinks

| Profile | Before delivered events/s | Grouped | Change | Before acceptance P99 ms | Grouped P99 ms |
|---|---:|---:|---:|---:|---:|
| gzip=false/clients=1 | 596.69 | 801.69 | +34.35% | 19.471 | 6.730 |
| gzip=false/clients=16 | 942.27 | 2201.74 | +133.66% | 58.798 | 33.178 |
| gzip=false/clients=64 | 1708.30 | 2156.73 | +26.25% | 42.122 | 48.849 |
| gzip=true/clients=1 | 1212.55 | 1207.74 | -0.40% | 2.868 | 2.952 |
| gzip=true/clients=16 | 1707.45 | 2152.20 | +26.05% | 13.556 | 13.396 |
| gzip=true/clients=64 | 1720.91 | 4631.08 | +169.11% | 105.298 | 28.007 |

The four concurrent profiles improve their delivered-rate medians, but plain/64-client acceptance P99 worsens by about 16%. These are observed workload-specific gains, not guaranteed production percentages. No low-load speedup is established by the inconsistent campaigns and controls.

## Why the default remains per-record

The earlier six-pair 8192-event campaign on `010e5c1` had an 11.6% lower plain/single-client delivered median. The new run has a higher baseline-to-candidate median for that profile, but its same-candidate-binary 1-versus-64 control goes the other direction. No code change was made to hide the unfavorable measurements or add artificial delay to the baseline. The rollout decision is conservative; it does not claim that changing the default repairs a proven timing defect.

| Same candidate binary, one client | Group=1 delivered/s | Group=64 delivered/s | Change |
|---|---:|---:|---:|
| plain-1 | 885.23 | 734.47 | -17.03% |
| gzip-1 | 1205.66 | 1205.71 | +0.00% |

### Previous campaign is retained, not replaced

| Profile | Previous before delivered/s | Previous grouped | Change |
|---|---:|---:|---:|
| gzip=false/clients=1 | 1058.22 | 935.83 | -11.57% |
| gzip=false/clients=16 | 1730.97 | 2191.51 | +26.61% |
| gzip=false/clients=64 | 1435.52 | 3000.11 | +108.99% |
| gzip=true/clients=1 | 596.71 | 769.72 | +28.99% |
| gzip=true/clients=16 | 1417.02 | 2942.81 | +107.68% |
| gzip=true/clients=64 | 1720.50 | 2164.51 | +25.81% |

The previous single-client gzip service median was also 84.6% slower. All corresponding service samples and same-binary controls remain in the archive. Compare each campaign only with its own paired baseline.

## Reliability and test quality

The final campaign independently audited **96 pipeline/control samples, 786432 timed accepted events and 1572864 sink deliveries**, excluding warmups. Every persisted producer/sink event, JSON field and type, unique delivery identity, HTTP outcome, latency quantile, policy and reported rate passed reconciliation. No missing/extra/duplicate or corrupted event was accepted. Per-file evidence hashes are retained. The auditor is separate from the benchmark runner and has negative controls for corruption, missing events, weaker policies and false performance claims. The earlier campaign was separately audited in the working environment with the same 96-sample totals.

Permanent delivery CI runs 27 executable cases for group=1/64 and seeds=127/991, for **108 case executions**. These include SIGKILL after acceptance, partially answered concurrent cohorts, retries, failed sinks, interrupted appends and kernel-enforced write refusal. Writer tests cover late arrivals, rotated files, shared errors, previous-success isolation, tag separation, mixed reliability, bounds, closure and cancellation during Sync. Twenty shuffled Journal race runs pass. Mutation controls reject omitted Sync, early success and ignored Sync errors, with an independent best-effort control still passing.

The new default-policy test intentionally failed the old automatic-grouping policy before the opt-in correction. That is a rollout-requirement change, not a claim that the old default was a newly discovered data-loss bug. Cancellation-during-Sync controls already passed before the correction.

## Incremental commits and verification

- `5350923`: default-policy and cancellation-during-Sync tests.
- `9b55321`: opt-in rollout correction; explicit policy configuration outside benchmark timing.
- `cdf1129`: independent persisted-evidence auditing and both-policy permanent delivery coverage.

Source validation: [build/vet/race/Docker](https://github.com/Laisky/go-fluentd/actions/runs/35899898806), [108 delivery cases and original negative controls](https://github.com/Laisky/go-fluentd/actions/runs/35899899365), [group measurements, audits and unsafe-mutation controls](https://github.com/Laisky/go-fluentd/actions/runs/35899898869), [35 component workloads](https://github.com/Laisky/go-fluentd/actions/runs/35899898842), [CodeQL](https://github.com/Laisky/go-fluentd/actions/runs/35899898738). All passed for the recorded measured source. Earlier campaign: [35881057476](https://github.com/Laisky/go-fluentd/actions/runs/35881057476). No merge or deployment.

## Reproduction and retained evidence

Keep the existing configuration and set `settings.journal.group_commit_max_messages: 64` only to opt in. Reliable HTTP requires the existing receiver's `require_durable_ack: true`. Omitted/zero/one grouping retains per-record Sync; explicit lossy/dry configurations are not covered by this reliable profile.

```sh
go build -mod=readonly -race -o /tmp/go-fluentd-delivery .
python3 tests/delivery/run.py --binary /tmp/go-fluentd-delivery --artifacts /tmp/delivery-per-record --seed 127 --group-max-messages 1
python3 tests/delivery/run.py --binary /tmp/go-fluentd-delivery --artifacts /tmp/delivery-grouped --seed 991 --group-max-messages 64
go test -mod=readonly -race -count=20 -shuffle=on -run '^TestJournal' ./internal/controller
python3 .scripts/verify_group_commit_contracts.py --evidence /tmp/group-barrier-controls
python3 -m unittest discover -s tests/performance -p 'test_*.py'
```

The read-only `.github/workflows/group-commit.yml` contains exact baseline builds, identical benchmark copying, all repetitions and the auditor command. [Compressed retained evidence](../tests/performance/results/20260923-group/paired-evidence.json.gz) contains both campaigns' raw journal output, individual numeric samples, full summaries/ranges, controls, manifests, binary hashes, environments, artifact digests and final ledger audits. Full per-request files and sink ledgers remain in the linked CI artifacts (14-day retention); rerunning the workflow regenerates them.

```python
import gzip, json
with gzip.open("tests/performance/results/20260923-group/paired-evidence.json.gz", "rt") as source:
    evidence = json.load(source)
assert set(evidence["campaigns"]) == {"pre-rollout", "opt-in"}
```

This validates the stated real-process/workload contracts, not all possible interleavings, unlimited backlog, arbitrary corruption, physical power loss or a real Kafka/Elasticsearch cluster. TCP ingress still lacks a durable ingress acknowledgement. No exactly-once guarantee is introduced.
