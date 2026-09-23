# Durable group-commit experiment

Baseline: `76717b288a170acc0e1e12daf19def9fa7da1bb1` (merged performance PR #11).

## Non-negotiable contract

A successful reliable HTTP acceptance still follows completed data writes and a successful journal `Sync()`. Every record in a group has its own immutable request-owned receipt. No receipt or live downstream publication precedes the group's barrier. Failed writes or a failed shared barrier must never report success for the affected group. Previously successful groups remain successful. A request cancelled/disconnected before its response has an unknown outcome, not a promise that its record is absent.

Group work is bounded. The initial candidate drains only immediately available messages, with no timer delay to fill a batch. A single request must make progress without a second arrival. Normal channel closure drains owned work; cancellation must not manufacture successful receipts. Distinct tags remain independent writers, and the on-disk format, rotation/recovery rules, confirmation lifetime, and downstream delivery contract remain unchanged. Best-effort traffic does not acquire a durable guarantee.

## Test and measurement gates

1. Extract the existing per-record writer without changing policy; test storage/barrier ordering, write/Sync errors, cancellation, ownership, and real-file reopen controls.
2. Measure the original and candidate with identical workloads, Go version, runner, payloads, reliability mode and work units. Report actual Sync calls per accepted record in a separate journal benchmark; do not call buffered throughput durable throughput.
3. Compare low concurrency and concurrent saturation, plain/gzip, both journal service cost and the ordinary executable's two fsynced downstream ledgers. Retain all paired samples, latency distributions, CPU, RSS, source refs and slower cases.
4. Run the unchanged crash/restart delivery suite plus new concurrent acceptance/crash/failure cases against the race-instrumented executable. Timing runs are uninstrumented.
5. Commit each validated stage separately. Reject or revise candidates whose apparent gain comes from weakened durability, missing output, fixture costs, or unacceptable low-load latency.

No result is claimed in this specification. The final measured report will link the exact tested revision and raw evidence. Kernel/process failure tests are not physical power-loss qualification; filesystem synchronization must be honored by production storage.
