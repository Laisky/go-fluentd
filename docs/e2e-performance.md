# End-to-end capacity and resource measurements

This work starts at merged master `828d34ab1460820cf732810d01b56985d46a4ab9` (source tree `20da7a24e7adf1a69c2373f3729c8e33e10b2ba8`). It does not replace the existing delivery/crash contracts.

## Measurement contract

Use the ordinary, optimized application executable, a separate standard-library Go load driver, and local simulated HTTP destinations. Do not benchmark a race-instrumented binary or infer delivery from an application counter. The driver validates each original event or opaque OTLP envelope at every required destination.

Report HTTP admission latency separately from end-to-end destination latency. Preserve per-request records, process CPU/RSS observations, settings, binary hashes, accepted/failed/dropped counts, duplicates, and complete trial results. CPU and RSS refer to the application process, not its load generator. Mock-destination acceptance does not certify durable storage at a real backend.

Provide both bounded closed-loop concurrency and independently scheduled open-loop arrivals. Include scheduling delay in open-loop end-to-end latency. Generator drops, rejected requests, and missing deliveries invalidate a successful-capacity claim; they must remain visible rather than being silently retried or excluded.

Use identical workloads and settings in alternating baseline/candidate pairs. Profiling is a separate diagnostic run because it adds overhead. Keep synchronization, payload preservation, response classification, and crash-recovery barriers unchanged. Passing throughput alone is not acceptance.

## Iteration and stopping rule

Measure the unmodified baseline first. Profile expensive paths, make one bounded change at a time, repeat the same end-to-end cases, and retain unsuccessful experiments. Require repeatable improvements larger than observed noise; do not trade correctness for performance. Finish with repeated paired measurements, open-loop load points, and native behavior/race/crash acceptance.

The stopping condition applies to the recorded workload matrix and explored changes, not a claim that no future hardware, workload, or algorithm could improve further. These local measurements are not a production capacity guarantee.

## Current execution checkpoint

The first 2,048-request OTLP baseline (512 synthetic payload bytes, concurrency 32, explicitly configured 10 ms replay cadence) measured about 9,834 admissions/s but only 422 fully delivered envelopes/s; end-to-end p99 was 4.58 s. This is an exploratory single sample, not the final paired result.

A separate CPU profile attributed 66.4% cumulative sampled CPU to `path/filepath.glob` in receipt lookup. An initial streaming-prefix experiment improved all three exploratory pairs. A startup inventory of only uncertain temporary receipts is under validation; it must preserve existing receipt formats and the no-resend-on-uncertainty contract.

Final measured tables, runnable harness, source identities, and acceptance evidence will be added to this PR before review readiness. Keep it Draft until then.

Method references: [Go diagnostics](https://go.dev/doc/diagnostics), [open versus closed load models](https://grafana.com/docs/k6/latest/using-k6/scenarios/concepts/open-vs-closed/).
