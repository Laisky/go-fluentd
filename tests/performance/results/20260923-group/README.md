# Group-commit evidence, 2026-09-23

See [the measured report](../../../../docs/group-commit-performance.md).

`paired-evidence.json.gz` is a deterministic gzip-compressed JSON document (schema 1), not an executable or a Git LFS pointer. It retains **both** the earlier mixed-result campaign and the opt-in revision:

| Campaign | PR source head | GitHub Actions run |
|---|---|---|
| `pre-rollout` | `010e5c1e53c89cab629f904ca155ab46212d0761` | `35881057476` |
| `opt-in` | `cdf1129e5a8cd3a270304f0a11ca0298ce3d0c39` | `35899898869` |

Each campaign has seven shards: journal service plus plain/gzip at 1, 16 and 64 clients. Every before/after comparison has six counterbalanced pairs. Pipeline samples contain 8,192 timed events with one excluded warmup; service samples contain 2,048 records. **2 KiB describes the payload, not the complete encoded message including metadata.** Service `ns/op` is amortized completed-message cost; pipeline P99 measures the durable HTTP acceptance response, not delivery to both sinks.

Each shard retains original numeric samples, summaries with min/max and paired ratios, binary hashes, hardware/filesystem provenance, artifact IDs and verified SHA-256 digests. The service shard includes original benchmark output and unsafe-barrier mutation controls. The final pipeline shards additionally contain independent ledger audits and source/sink/configuration hashes. Single-client shards retain same-candidate-binary group=1/64 controls. No unfavorable sample is filtered out.

```python
import gzip
import json
import statistics

path = "tests/performance/results/20260923-group/paired-evidence.json.gz"
with gzip.open(path, "rt") as source:
    evidence = json.load(source)

shard = evidence["campaigns"]["opt-in"]["shards"]["plain-16"]
for variant in ("before", "after"):
    values = [row["metrics"]["delivered_per_second"]
              for row in shard["samples"] if row["variant"] == variant]
    assert len(values) == 6
    print(variant, statistics.median(values), min(values), max(values))
```

Full per-request traces and on-disk peer ledgers are in the linked run artifacts, with 14-day retention. They are deliberately not duplicated into Git. The permanent read-only group-commit workflow regenerates the complete evidence and runs `audit_group_evidence.py` after timing. Stored hashes document the audited bytes but are not substitutes for those bytes when repeating a full ledger audit.

All comparisons are within one profile on the same hosted runner. Different profiles use different VMs, so this is not a cross-profile scaling curve, production-device capacity estimate or physical-power-loss qualification. This dataset documents an opt-in throughput/latency tradeoff, not a universal performance improvement.
