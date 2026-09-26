# Corrected selective replay: adoption and preserved performance evidence

## Published dependency and merge order

This increment continues go-fluentd PR18 from `5489c5a4660613adabf4861d2b94631198b17d78`.
It recovers the previously local-only consumer sequence test and fixes the journal
pin, rather than recreating a competing implementation or reviving an old worktree.

The journal PR8 merge `8758f84c23d9ae29bf800f9e84d2ec64125008aa` did not include the
sequence correction found in the previous experiment. The correction is published
in [go-journal PR9](https://github.com/Laisky/go-journal/pull/9), on the existing
journal branch, at `fc156a60fafc21dd36ad534e5b8f7a971beccec8`.
**Merge that owning-repository correction before merging this application adoption.**
Neither repository is automatically merged or deployed by this work.

The committed application requires:

```text
github.com/Laisky/go-journal v1.1.7-0.20260926040517-fc156a60fafc
h1:bL1gqp8g8FPymRwWnrHRguChTY2gVXdHw6IqwL7cajI=
```

Go 1.27.1 resolved this version and checksum in journal run36216816485, artifact
10898096599. The artifact ZIP SHA256 is
`8dc9c2d7002453442103224de0446ef9c8c1b01615c2b612c1b83fb385693320`.
The ordinary candidate uses committed go.mod/go.sum, not a local replace,
alternate modfile, vendored fork or workspace override. Only the journal require
line and its two checksum entries change; other application dependencies and
production source, synchronization/ACK/retry behavior, driver and README remain.

## Native adoption gate repaired and executed

Continuation checkpoint: `1a54dd4b3e119a16e783cf304449ca06e11e311a`, tree
`af7ff6321ec5977482dc75f6dc1775475820dd8f`. This resumes the already published
`c3b953c9` dependency/test/report increment; it does not repeat the prior local
implementation or performance campaign.

The first adoption workflow, run36218163861, failed safely: the original and
corrected eight-case controls passed, but the merged-PR8 control stopped during
compilation because its temporary go.sum lacked the module-content hash. Zero
behavioral failure cases executed. The failed artifact10898019008 and its35-file
manifest are retained; a compiler failure was not counted as a detected regression.

Control setup now explicitly downloads the selected journal using that control's
modfile and requires both content and go.mod checksums in the matching sum file
before readonly tests. Cache priming in the resolver is not treated as proof the
control sum file is complete. Failed control manifests are retained too. The
oracle requires each of the eight exact cases once, and the correct semantic
assertion in every failing leaf. Build errors, timeouts, panics, races, unrelated
failures, skipped cases and duplicate/missing executions are rejected explicitly.
These checks also remain active with Python optimization enabled.

Nine Python tests cover the oracle and an isolated file:// module-cache fixture.
That synthetic fixture alone disables sumdb; real dependency adoption continues
to use the normal Go checksum verification. Local tests used installed Go1.23.2
for this small fixture, not as a substitute for application Go1.27.1 acceptance.
Removing the explicit download recreates the missing-content-checksum assertion;
restoring it passes. No production source, dependency pin or historical CSV changed.

Fresh native Go1.27.1 run [36243801400](https://github.com/Laisky/go-fluentd/actions/runs/36243801400)
completed the following on the exact implementation checkpoint:

| Check | Result |
|---|---|
| Complete native module graph |220 modules; no Replace/Error; only journal version differs across controls|
| Original dependency sequence cases |8 pass|
| PR8 merged dependency sequence cases |8 fail their own intended payload/ID assertions|
| Corrected committed dependency sequence cases |8 pass|
| Complete application suite |1,231 test/subtest passes; no failing/skipped test cases|
| Three focused selective-recovery race repetitions |48 test/subtest passes|
| Adoption-oracle unit tests |9 pass|

Packages reporting `[no test files]` are not counted as passing or skipped test
cases. Downloaded artifact10906497595 has ZIP SHA256
`ecf30facdabf22894204e8fbfd5bbefdca2a3bf000979942964b365bb8af482b`.
All45 manifest entries and297 source files reconstruct the exact tree. Offline
rechecking confirms each control's cases/checksums, graph membership/versions and
unchanged candidate manifests. These are fresh native adoption results, not a new
sustained-performance experiment. Subsequent documentation commits must be checked
against their own hosted runs before final review status is updated.

## Regression and conservative correction

The old generated decoder retains fields missing from the next envelope in its
reused Data object. An acknowledged ID9/payload A followed by an ID10-only envelope
must therefore return A with ID10. A following envelope missing ID must preserve
ID9 and its ACK suppression, not inherit a caller sentinel. These layouts are
accepted by the old decoder; normal WriteData emits both fields.

The initial selector skipped the acknowledged record without preserving that
state. Falling back only for the subsequent noncanonical envelope was insufficient.
The corrected selector skips only if the next complete canonical envelope is
already buffered and guarantees replacement of both fields. Before unfamiliar,
cross-buffer or segment-boundary input, materialize the preceding record normally.
Lookahead precedes ACK lookup to avoid double-consuming a consume-once callback.

No borrowed payload strings, additional payload-state pool, maximum-ACK shortcut,
format/API change, omitted validation or removed durability barrier is introduced.
Exact membership controls suppression. Sparse ACKs, Data-before-ID and reversed
layouts, compression, partial tails, corruption, large fallback records and owned
pending payloads retain their existing tests and semantics.

## Permanent native adoption gate

The new read-only `Corrected journal dependency adoption` workflow runs
`tests/loadtest/verify_journal_adoption.py` with Go1.27.1. It checks the corrected
source blob and complete native module graph, refusing any Replace or Error entry.
Controls alone use temporary modfiles that select other published module versions;
no application or journal source is patched, and the candidate manifests must not
change. It requires the same eight public regression cases in all three runs:

| Version | Required result |
|---|---|
| Original pinned612f5354 dependency | All eight pass |
| PR8 merged8758f84 dependency, resolved by Go | All eight fail named payload/ID assertions |
| Corrected immutablefc156a60 dependency | All eight pass |

All three module graphs must contain the same modules and differ only in journal
version. A compiler error, timeout, skipped test or absent assertion does not
count as detecting the regression. The workflow additionally runs the complete
application suite and three focused selective-recovery race repetitions. Existing
full-race, process-recovery, Collector and load workflows remain independent gates.
Inspect their exact head, not the previous revision's green status.

The journal's own exact-head native acceptance recorded343 full-suite passes,
1,029 shuffled race passes,131,439 sequence-fuzz executions and8 intended unsafe
variant failures with passing public and differential controls. Its4 workflows
passed before application adoption. These are fresh journal results, not a new
application performance campaign. Current application results are retained in its
own artifact and PR status, rather than guessed before CI completion.

## Preserved earlier experimental campaign (not rerun at publication)

The following six trials were executed before publication using an explicit local
replacement of the corrected journal source. All owning Go source files in the
now-published module match that corrected source. This source comparison does not
turn the older local-replacement experiment into a new published-module benchmark.
The current native gate closes dependency/correctness provenance separately.

The comparison was original journal versus selective replay plus the sequence
correction, **not** correction versus the unsafe selector. Go1.27.1/Linux amd64,
four-core cgroup/4GiB, GOMAXPROCS=4, shared host, ordinary executable, real plain
per-record-synchronized journal, independent local HTTP mock. Each trial used
300,00016KiB Unicode NDJSON records plus64 untimed warmups, end-to-end concurrency
128, identical driver/settings and serial AB/BA/AB. Profiling, compilation and
other tests were excluded from these timed runs.

| Median metric | Original journal | Corrected candidate | Change |
|---|---:|---:|---:|
| Delivered records/s | 9,704.51 | 10,184.48 | +4.95% |
| App CPU microseconds/record | 229.93 | 215.30 | -6.36% |
| Destination P99 ms | 27.180 | 25.435 | -6.42% |
| Admission P99 ms | 6.227 | 5.773 | -7.29% |
| Peak RSS MiB | 204.36 | 172.86 | -15.41% |
| Cumulative allocation bytes/record | 66,887 | 48,865 | -26.94% |

All1,800,000 measured requests were accepted/accounted for, with no missing,
failed or generator-dropped request and six clean exits. One baseline had37
identical duplicate observations; those are retained at-least-once behavior.
All six qualified under the unchanged steady gate: mean CPU54.05-55.82% of the
effective four-core quota,91.30-100% qualifying windows, rateCV0.0668-0.1576,
23-25 trimmed seconds and at most128 incomplete deliveries. All three pairs
improved throughput, CPU, destination P99, RSS and allocation; one admission-P99
pair regressed0.94%. No sample was dropped. This is bounded closed-loop evidence,
not certified sustained offered rate, statistical confidence or a production SLO.

[The six full-precision rows](../tests/loadtest/results/20260926-selective-sequence.csv)
and [frozen workload](../tests/loadtest/cases/selective-sequence.json) are recovered
unchanged. The prior archived source/patch/bundle and raw measurements remain the
record of those runs. Older host/baseline measurements are not pooled with them.

Prior local correctness recorded343 journal tests/1,029 race passes,165,339 sequence
fuzz executions and1,231 application tests/3,693 race passes using the local
replacement; the original dependency with new regression tests also passed1,231.
Those are historical validation, not additional fresh runs in this publication.
Previously incomplete `go list -m all` was not represented as a complete graph;
the new native workflow now explicitly requires complete graph comparison.

## Reproduction and remaining costs

After checking out the desired application commit, use its committed module graph:

```sh
go mod download
go mod verify
python3 tests/loadtest/verify_journal_adoption.py /tmp/journal-adoption
go test -mod=readonly -count=1 ./...
go test -mod=readonly -race -shuffle=on -count=3 -run Selective ./internal/controller
```

For new performance measurements, build the original baseline and current candidate
with the same native toolchain, then run the unchanged compare.py and the frozen
case above. Keep diagnostics separate, retain all qualification failures and
report the new host/baseline instead of relabeling the table above.

Lookahead adds validation CPU, especially for fully pending records; boundary
records still materialize. The prior corrected64-record16KiB microbenchmark kept
about16,436B at100% ACK instead of claiming zero allocations. Unknown/large records
fall back. No receipt compaction/TTL/aggregate quota, memory erasure, exactly-once,
backend-fsync or physical-power-loss guarantee is added. Preserve WAL/generation/
receipts together and never let two processes write the same journal directory.
