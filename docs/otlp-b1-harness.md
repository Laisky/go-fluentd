# B1 journal harness: behavior CI versus host qualification

The historical [b1 measurement](otlp-b1-isolated-capacity-20261001.md) records
one bounded run and its unresolved production capacity gate. Its measurements,
source checksum and binary checksum apply to that historical source, not to
this revised harness. No production runtime, resource limit or deployment is
changed by the harness follow-up.

## Refusal and delivery contracts

`rejectScanBudget` returns a joined error containing both
`ErrOTLPJournalCapacity` and `ErrOTLPJournalScanBudget`. The old harness checked
only the broad capacity sentinel. The revised test checks the specific scan
cause first and records disjoint `byte_limit_refused_per_signal` and
`scan_budget_refused_per_signal` maps. `refused_per_signal` remains the total
for consumers of the previous log format.

A byte-capacity run fails after logging evidence if either its classified scan
refusals or the independent journal scan-rejection counter is nonzero. Scan
exhaustion must never qualify as proof of byte-limit refusal. Default-suite
regressions cover wrapped/joined sentinels, unexpected errors, and real `Admit`
with deterministic entry-budget/deadline injection at the existing scan seam.
They assert no WAL write, identity advance or sticky fault on scan rejection.

Successful delivery must match the entire admitted original-payload set with
exactly one delivery per payload. Lost, duplicate, changed and unadmitted
payloads all fail the oracle. This is a synthetic-run invariant, not a general
end-to-end exactly-once guarantee across ambiguous network/crash outcomes.

## Fast, repeatable CI

The `OTLP edge acceptance` workflow runs separate Linux jobs for paced and
unpaced fast validation. Each compiles the tagged measurement and executes it
and all four `TestRegressionB1*` contracts three times with race detection and
randomized test order. The evidence checker requires every top-level test and
every expected phase to execute all three times, rejects failures/skips, and
reconciles mode flags, refusal counts and final delivery counts. Its own unit
tests include empty logs, missing/duplicate passes, wrong modes, invalid scan
bounds, misclassified refusals and incomplete final reports.

Fast validation uses 5ms pace/hold sleeps and a **1s storage scan timeout** to
avoid turning shared-runner scheduling into a timing SLA. It keeps 1024 entries,
128KiB WAL / 512KiB root admission thresholds, receipt GC, batch 8 and the 108
original requests. Both the mode and actual scan bounds are recorded in each
phase. This validates behavior only, not the production 10ms scan budget,
b1 performance, memory isolation, or business latency. Deterministic deadline
regressions still exercise the real admission timeout path.

Run the same fast suite locally from the repository root:

```sh
state=$(mktemp -d)
export OTLP_MEASURE_STATE="$state"
export OTLP_MEASURE_FAST_VALIDATE=1
for paced in 0 1; do
  export OTLP_MEASURE_PACED="$paced"
  go test -mod=readonly -tags=otlp_b1_measure,timetzdata -race \
    -count=3 -shuffle=on -timeout=180s -json \
    -run '^(TestOTLPB1IsolatedCapacity|TestRegressionB1.*)$' \
    ./internal/controller > "$state/tests-paced-$paced.jsonl" || exit 1
  python3 .scripts/verify_b1_measure.py "$state/tests-paced-$paced.jsonl" \
    --repeats 3 --paced "$paced" || exit 1
done
```

CI retains source SHA, toolchain, dependency manifests, JSON test logs and each
run's dedicated synthetic state as artifacts, even on failure. It never connects
to b1 or any production peer. The test leaves its state in place; the local
command likewise does not delete evidence.

## Explicit host qualification remains separate

For an actual host qualification run, unset `OTLP_MEASURE_FAST_VALIDATE` and set
`OTLP_MEASURE_PACED=1`. Normal mode retains 500ms/1000ms pace/hold intervals and
the original **1024-entry / 10ms** scan budgets. Apply the historical report's
external CPU/memory/network/filesystem and watchdog bounds before running it;
the Go test alone does not impose those container limits. A scan rejection
invalidates the byte-capacity run instead of being relabeled as a byte refusal.

No host run was repeated merely to update CI. Production promotion still needs
representative sustained ingress with concurrent replay, real TLS/backend
persistence and correlation, crash durability, source privacy and business
streaming/nonstreaming/error/metric-cycle acceptance. Merging these test-only
changes does not clear that gate or establish a 24-hour/zero-loss guarantee.

The current harness reserves future receipt/replay work under the finite
whole-root policy: 16 MiB root admission and a 256-byte synthetic response bound.
The 128 KiB WAL threshold and independent 16 MiB actual-use assertion remain.
Historical reports retain the settings used when those measurements ran.
