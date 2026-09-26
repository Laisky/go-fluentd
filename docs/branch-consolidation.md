# Branch consolidation and current work

The current performance work belongs only on **PR #18** and
`perf/e2e-load-20260925`. PR #17 has been merged; do not resume performance work
on its old feature or preparation branches.

- [PR18 workspace reconciliation](pr18-workspace-reconciliation.md): current
  baseline, archived alternatives, selectively recovered tests and safe
  continuation rules.
- [Historical PR17 consolidation snapshot](branch-consolidation-pr17.md): the
  original 36-tip inventory, preserved byte-for-byte. Its PR status and
  continuation instructions describe the 2026-09-25 checkpoint, not current work.

Recheck the actual remote head and local worktrees before editing or pushing.
Retain dirty work and rejected experiments; never replace a newer branch with an
old local candidate or infer missing work from different commit IDs alone.
