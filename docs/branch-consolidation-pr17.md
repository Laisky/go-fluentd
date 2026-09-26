# Branch consolidation into PR #17

## Canonical work branch

Continue this work only on `feat/otlp-http-foundation-20260924` / PR #17.
Do not restart a prototype from an old `ci/`, `chore/` or transfer branch.
The inventory below records all **36 remote branch heads** observed on
2026-09-25. Commit IDs are immutable audit anchors, not instructions to merge
old dependency graphs. Historical branch refs are preserved; they are not
additional active delivery branches.

PR #16 was retargeted to the #17 branch and merged there, preserving its eight
commits in `195de0e3490317123722cbd6cbab19796c859169`. Its ordered parents are
`834dc931abb0509a63383eacf212f3f1e59656a0` (OTLP) and
`5125a082068186edd786e268b3c937f06501c3f3` (CloudEvents/NDJSON).
The combined tree is `fe3635e9c10ff6debe8f9be9c75331df47b5228c`.
This merge targets the PR work branch, **not master**. Master remains
`f053ffb43cc92c81d7d28f586b1825d15b324d23` at this checkpoint.

## Audit method and recoverability

Compare each tip with #17 using ancestry, its merge base, complete net file
diff and unique commits; use blob equality to identify transplanted changes.
Do not treat divergent commit IDs alone as missing work. Inspect encoded
transfer artifacts separately and never execute unverified fragments.

A complete Git bundle and all observed refs were exported before consolidation
by read-only run [36088815960](https://github.com/Laisky/go-fluentd/actions/runs/36088815960),
artifact 10845141293. The preserved `source.bundle` SHA-256 is
`1c1cdd9e09fe179400efb5ef9ca163f92b3dd900303e54e6ab6d835dcb217634`.
It preserves every listed tip, including malformed transfers and unrelated
branches, without importing their debris into product history. The downloaded
ZIP digest is `ecd337093a185ae4d4eb60bca87ad804988513e2f39391bca2e6d2d0ef1bdec9`.
The workflow artifact has finite retention; retain the delivered evidence bundle
for long-term recovery. The temporary export workflow was removed from its
pre-existing preparation branch in `5d124fb755b16787cc3169b685e7262f84afd07a`;
it never entered #17. No branch ref is deleted by this consolidation.

## Per-branch disposition

| Branch | Snapshot tip | Disposition | Decision |
|---|---|---|---|
| `build/update-go-journal-20260924` | `b0903bd5d113754d8f220210a66f6eb69991d47c` | Included | Tip is an ancestor of the initial #17 head. |
| `chore/group-review-workspace-20260923` | `b56d48cc2554e7dfef8d30828432971fdd037103` | Net-zero | No net changes from its common base; completed temporary workflow history. |
| `chore/journal-dependency-preflight-20260924` | `a150a9e859b2ef78a237554eeefe99315a5d825e` | Superseded | Consumer regression already integrated by #15; this draft adds only an extra unmatched closing brace. |
| `chore/modern-formats-preflight-20260924` | `42ba864e1c588781645ef616a7a331d00cc2b719` | Transfer only | Incomplete encoded transfer and dependency preflight; not executable acceptance. |
| `ci/event-stage-1-20260924` | `d0af0930a2d3abb09fb85efe1b12e39693bfd9de` | Merged via #16 | All original event commits retained by merge 195de0e34903. |
| `ci/event-stage-2-20260924` | `c2eda9ce8a996759f7b16975cb0fd4d6d8951a2a` | Merged via #16 | All original event commits retained by merge 195de0e34903. |
| `ci/event-stage-3-20260924` | `5125a082068186edd786e268b3c937f06501c3f3` | Merged via #16 | All original event commits retained by merge 195de0e34903. |
| `ci/msgp-fix-validated-20260924` | `7cf9f9074782b3bbf947c9686176d74231d2b733` | Included | Tip is an ancestor of the initial #17 head. |
| `ci/otlp-collector-candidate-20260925` | `c608b41c6952d8f2a34e84df02a69c4a182dff50` | Included | Tip is an ancestor of the initial #17 head. |
| `ci/otlp-config-workspace-20260924` | `bc36d36a2e272eea07f74840d87bd40895ad37e2` | Superseded prototype | Recovered configuration prototype; replaced by strict settings.otlp and environment-named secrets. |
| `ci/otlp-foundation-publish-20260924` | `d14049b8fba7bc53a83345507cd426b7e3142264` | Transfer only | Incomplete encoded foundation transfer and temporary publisher. |
| `ci/otlp-gc-interop-20260925` | `ffcdceb2ed97d42ebfdf502d1fdc128769a0f386` | Equivalent | Literal decoder regression is byte-identical to #17. |
| `ci/otlp-journal-validated-20260924` | `2990a76bb74d9b5a835ec45f87ec2e76a6258f20` | Included | Tip is an ancestor of the initial #17 head. |
| `ci/otlp-lifecycle-workspace-20260924` | `58d4b860e123fb80c976469e7ed89c0667604b59` | Net-zero | No net changes from its common base. |
| `ci/otlp-native-20260924` | `ac63c294093144e95dfa73655e5487f564ec377b` | Equivalent | Event feature is now included; only extra net file is a temporary native-dependency workflow. |
| `ci/otlp-routing-source-20260924` | `d679aef901c7f401851f757664b960b3dbfd9f07` | Net-zero | No net changes from its common base. |
| `ci/otlp-service-publish-20260925` | `c07166b6f73d1581e15d89abfd3b5eaafc70e745` | Transfer only | Existing preparation branch reused only for this read-only snapshot; temporary workflow removed in 5d124fb755b1. |
| `ci/otlp-service-validated-20260925` | `b82994453f0b601c9c121eb10868fc1490fb4d09` | Included | Tip is an ancestor of the initial #17 head. |
| `ci/otlp-state-validated-20260924` | `2db2b4330a6c84af67a2859b4a080e27fde3ce3d` | Included | Tip is an ancestor of the initial #17 head. |
| `ci/publish-stream-formats-20260924` | `d02c2d6bdc8c6f35558603237b3239a646993cb1` | Superseded prototype | Complete codec bundle head is included; incomplete adapter transfers are not safe production patches. |
| `dependabot/go_modules/github.com/gin-gonic/gin-1.9.0` | `75c67c2bffcd3227e01ad05106f02d8f54d54781` | Out of scope | Historical third-party dependency PR; retained unchanged. |
| `develop` | `2da6637965b04a071c25cccafb49c3043a13722d` | Out of scope | Historical development branch, not an interrupted branch from this task; retained unchanged. |
| `docs/readme-quickstart-20260923` | `bb888969d66848ff321d289debbfedbe540170e5` | Included | Tip is an ancestor of the initial #17 head. |
| `docs/restore-original-architecture-diagram` | `3cbf0880558e2b9fdccc3ed47141f28d0cfe99c2` | Included | Tip is an ancestor of the initial #17 head. |
| `feat/http-event-formats-20260924` | `5125a082068186edd786e268b3c937f06501c3f3` | Merged via #16 | All original event commits retained by merge 195de0e34903. |
| `feat/otlp-http-foundation-20260924` | `834dc931abb0509a63383eacf212f3f1e59656a0` | Included | Tip is an ancestor of the initial #17 head. |
| `fix/go127-reliability` | `aa4128f53e51b0ed68f60ed755b9018621c06b56` | Included | Tip is an ancestor of the initial #17 head. |
| `master` | `f053ffb43cc92c81d7d28f586b1825d15b324d23` | Included | Tip is an ancestor of the initial #17 head. |
| `perf/durable-group-commit-20260923` | `8078525f92151dcf2d584ef89adbc17f07110044` | Included | Tip is an ancestor of the initial #17 head. |
| `perf/group-commit-validation-snapshot-20260923` | `5e920dd7743a317ceee756e83c1fbbb99d3542cf` | Transfer only | Temporary validation snapshot workflow only. |
| `perf/measured-journal-20260923` | `86622a72aedfe7806280912b7668bdfd08399f8f` | Transfer only | Temporary dependency/workspace workflows only; measured product changes already integrated. |
| `perf/measured-pipeline-20260923` | `e33aa9bda7f768a28082ef7e8e28bd95581d18cf` | Included | Tip is an ancestor of the initial #17 head. |
| `test/component-behavior-20260922` | `cc1d4cffe9e5f96237a978169251fef17b1a9f81` | Included | Tip is an ancestor of the initial #17 head. |
| `test/component-behavior-coverage` | `8e93b3be99429e861530c2fb2f4107f0b1cb3845` | Included | Tip is an ancestor of the initial #17 head. |
| `test/e2e-delivery-20260922` | `2ba4c786e19a72c13ae26d74b98a3ddebe6d1b92` | Net-zero | No net changes from its common base; superseded closed test PR. |
| `test/end-to-end-delivery-20260922` | `5af24a88b173e7e60071e7c37e5060db614cdcea` | Included | Tip is an ancestor of the initial #17 head. |

Sixteen heads were already ancestors, four additional event heads are now
ancestors through #16, four histories have no net changes, three carry already
integrated/corrected content, seven are transfer or superseded prototype branches,
and two historical branches are outside this task. Some categories include
temporary workflow files; those files are not product features.

## Prototype decisions, not silent omissions

The complete `codecs-complete` transfer reconstructs codec commit
`2fc21bf7b3866fec07cd60abf7fb66f2fcd0d71f`, already in #16 and now #17.
The complete configuration transfer is an earlier 30,346-byte alternative
implementation. It uses a different configuration layout and inline bearer
secrets; the current tested service supersedes it. Do not overwrite the final
strict parser, dedicated listener, environment-secret references or saved plans.

Other transfer streams fail base64 or compressed-stream validation. Partial
forensic output is not an authenticated complete patch. The old `StreamHTTPRecv`
prototype includes best-effort/gzip alternatives, but the adopted `http_events`
receiver deliberately requires durable receipts and currently rejects request
gzip. That unsupported feature is documented, not represented as implemented.
Do not revive an alternate receiver merely to make every old file reachable.
Original bytes are preserved in the bundle, not dropped as supposedly empty.

The preflight journal regression differs from the integrated version only by
an extra unmatched closing brace. Importing that draft would regress the build.
The independent decoder test on the GC interop branch is exactly the current
blob. Net-zero branches require no source merge. `develop` and the old Dependabot
branch are unrelated and must not downgrade this dependency graph.

## Combined behavior and confirmed integration correction

The [same-process harness](../tests/combined_formats/run.py) runs all four event
output profiles and all three OTLP signals in both encodings concurrently in
one configured executable. A destination outage precedes SIGKILL; both distinct
journals must recover, then new records must progress before clean SIGTERM.
Plain and gzip WAL cases each require 8 event records and 12 OTLP envelopes,
exact values/bytes, independently scoped credentials and local acceptance.
Deleting either pipeline's successful sink evidence must fail the auditor.
These small cases do not measure throughput or prove physical-power-loss safety.

This test exposed a real JSON configuration defect: Viper decodes integer
literals as float64, which the OTLP parser rejected. The real-parser regression
fails before the fix and passes afterward. `1ce9a6d22a0a43d606d8fb0385d0630c1d55dc6d`
accepts only finite integral values within precision and target-type bounds.
Fractions, unsafe magnitudes, numeric strings and unitless durations remain
rejected. See [JSON configuration compatibility](otlp-service.md#json-configuration-compatibility).
No dependency change or disabled test gate is needed.

## Continuing and reviewing

The PR body records final-head CI results; do not reuse an older green run as
acceptance for a changed tree. Retain both event and OTLP workflows, the decoder
GC regressions and the same-process test. Future edits belong on #17's branch,
not on the archived preparation branches. Review storage/receipt growth,
listener security and at-least-once semantics before any explicit master merge.
