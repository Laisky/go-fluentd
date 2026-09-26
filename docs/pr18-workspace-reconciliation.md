# PR18 workspace reconciliation

## One active continuation

Current task: `Laisky/go-fluentd` PR #18, branch `perf/e2e-load-20260925`.
The original reconciliation started at `0d704e255d2f2e3e095eebb24c0959db06de20f4`,
tree `861850a9b89b518d8f4a0e4e831ce129b81e80d6`.
Recheck the remote ref before every push; publish only a non-force fast-forward
that retains intervening work. Do not create another staging/publishing branch.

The old [PR17 branch audit](branch-consolidation-pr17.md) remains a historical
36-tip inventory, not an instruction to resume work on the merged PR.
A fresh GitHub branch-list read on 2026-09-26 returned 37 heads: those 36 names
plus this performance branch. Relative to the old immutable-tip table, only
`master`, `feat/otlp-http-foundation-20260924` and
`ci/otlp-service-publish-20260925` advanced; those transitions are the already
recorded feature merge and snapshot-workflow cleanup. Other historical refs
have no new tips to integrate. No branch was deleted or rewritten.

A search of the current runtime's `/mnt/data`, `/tmp` and `/home/oai`
(to depth six) found no pre-existing Git worktree or repository to prune.
The surviving prior work is in mounted evidence archives, not live worktrees.
This continuation restored one active checkout from the exact current-head
CI source archive and validated its tree. A separate bare history repository
is archival only. It has no working tree or live publishing role.

## Corrected-dependency continuation checkpoint

Active application checkpoint: `1a54dd4b3e119a16e783cf304449ca06e11e311a`;
read PR18's current ref before continuing past documentation-only descendants.
The interrupted work had already published `cf11a6b0` (sequence tests), `26fcf0e7`
(corrected journal pin and adoption gate), and `c3b953c9` (preserved measurements).
They were inherited, not recreated or cherry-picked again. The remaining failed
native-adoption gate is corrected and tested in `1a54dd4b`; details and evidence
are in [the adoption report](profiling-selective-sequence-validation.md).

PR8 is merged, but its sequence correction is the separate, still-unmerged
[go-journal PR9](https://github.com/Laisky/go-journal/pull/9), on the existing
`perf/selective-replay-pr18-20260926` branch. The application already pins its
immutable `fc156a60` revision without a replace. Merge that correction before
PR18; do not revive the unsafe PR8-only pin or apply the old local bundle blindly.

The new branch inventory still has37 application branches and no new staging or
publishing branch. Historical tips remain the archived entries above; this step
changes only PR18. No live checkout existed in the inspected runtime locations
before restoration. One active application checkout was restored from the failed
native-run source archive, checked against the exact remote tree and aligned to
verified remote commit objects. Its local reconstruction history is archived
before alignment, rather than deleted. No alternate library worktree is needed
because its correction is already published. Evidence ZIPs are archives, not
active worktrees. Keep this single execution path and the owning PR9 relationship.

## Retained alternatives and selective recovery

| Retained object | Disposition |
|---|---|
| Sustained-workload local head `3ec74ab915e51c1bc61f4b1e5af9cd1b88a32bf9` | Its final tree was already published through seven API-created commits ending at `a2a5b686`; no duplicate cherry-pick. |
| String-copy local head `5d4bd65a819dd22b2e73bcc458575a68b850726b` | A distinct alternative to the published string-materialization implementation; archive only, not a pending merge. |
| Receiver alternative `126f4b5bfad8d385fd0f1588e8e5b19a2510a5a5` / `0e64dba702e5db531bf3cf53bd119feec099ee50` | Small-positive-length pooling with zeroing; not the all-hints implementation already published in `936cfc5d`. Preserve its different measurements and tradeoffs, do not overwrite the adopted implementation. |
| Unapplied `scratch_independent_test.go` from the prior receiver evidence | Recover as `internal/recvs/http_events_recovered_test.go`, against the actual published APIs. This adds protection without merging the alternative implementation. |

The recovered tests cover five encodings with deliberate scratch overwrite,
72 size/hint/limit combinations against the frozen private reader, late read
errors and invalid batch suffixes, concurrent request isolation, and a bounded
differential fuzz target. Their fresh acceptance results are recorded in the
PR, not copied from the earlier experiment. No previous performance table is
relabelled as a new measurement.

Original incremental bundles are retained byte-for-byte in the continuation
evidence, with these hashes:

- `go-fluentd-pr18-continuation-verification.bundle`: `adeff9c44a0bae445a2a5cb7e158b89c919fa5d5d4f6eebf68149917aeb519f1`.
- `go-fluentd-pr18-receiver-scratch-evidence.bundle`: `e354a45828d4d8858d464cbf446ec14f082a6a7ab5764b6d6686a8febdefa619`.
- `go-fluentd-pr18-sustained-commits.bundle`: `ee960837ccdd1e0e81c39f9dc6612f2d220d2e6bfb42115a64734987122a4f8d`.

The original 36-tip repository bundle is also retained; its SHA-256 is
`1c1cdd9e09fe179400efb5ef9ca163f92b3dd900303e54e6ab6d835dcb217634`.
Standalone incremental bundles may require their recorded base commit. Preserve
the base/source snapshots too; do not assume that a bundle is a standalone clone.

## Next continuation rules

1. Read PR18 and the exact remote head, then inspect `git status` and
   `git worktree list --porcelain` before editing. Preserve dirty work before
   changing or removing any checkout.
2. Compare ancestry **and** file content. API-created commit metadata can differ
   while trees match; parallel same-purpose experiments can differ in behavior.
3. Only integrate unique, tested value. Archive rejected or superseded candidates
   with their patch, base, measurements and decision. Never force-push an old
   experimental branch over a newer published implementation.
4. Keep diagnostic profiles separate from timed trials and retain failed
   qualification gates. A small benchmark improvement does not establish an
   application improvement. Do not remove persistence or ownership checks to win
   a benchmark.
5. Keep a single current PR description and this historical disposition ledger.
   No automatic merge, deployment or destructive remote-branch cleanup.

This reconciliation changes no production path, journal format, dependency or
historical result. Physical deletion of old remote refs is deliberately separate
from consolidating the active implementation and execution path.
