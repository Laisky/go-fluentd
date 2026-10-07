# Archived development branches

Recovery metadata only. DO NOT MERGE this archive commit into master or develop.
Every original branch tip is retained through an explicit parent of this archive commit.
BRANCHES.json maps each retired branch name to its exact SHA. No experimental source was merged.

Restore one branch in a clone:
```sh
git fetch origin refs/tags/archive/development-20261007:refs/tags/archive/development-20261007
git show archive/development-20261007:BRANCHES.json
# Select the exact SHA from the manifest; never guess it.
git branch recovered-branch-name <manifest-SHA>
```

An independently verified, self-contained Git bundle is retained in the cleanup artifacts.
Original release tags, master and develop are unchanged. User-host worktrees are outside this cloud runner.
