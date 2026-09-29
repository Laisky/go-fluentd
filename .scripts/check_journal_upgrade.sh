#!/usr/bin/env bash
# Check journal wire/disk compatibility using separate old-writer/new-reader processes.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

# This is the pin from master before the September 2026 dependency upgrade.
# The remaining dependency graph stays identical to isolate the journal change.
previous='v1.1.7-0.20260926040517-fc156a60fafc'
evidence="${1:-$(mktemp -d)}"
mkdir -p "$evidence"
evidence="$(cd "$evidence" && pwd)"
fixture="$(mktemp -d)"
trap 'rm -rf "$fixture"' EXIT

cp go.mod "$evidence/previous.mod"
cp go.sum "$evidence/previous.sum"
go mod edit -modfile="$evidence/previous.mod" -require="github.com/Laisky/go-journal@$previous"
go mod tidy -modfile="$evidence/previous.mod"
go list -modfile="$evidence/previous.mod" -m github.com/Laisky/go-journal > "$evidence/previous-version.txt"
go list -mod=readonly -m github.com/Laisky/go-journal > "$evidence/candidate-version.txt"

FLUENTD_JOURNAL_UPGRADE_MODE=seed FLUENTD_JOURNAL_UPGRADE_DIR="$fixture" \
  go test -modfile="$evidence/previous.mod" -count=1 -timeout=180s -v \
  -run '^TestRegressionJournalUpgradeDiskFixture$' ./internal/controller \
  2>&1 | tee "$evidence/old-writer.log"
FLUENTD_JOURNAL_UPGRADE_MODE=check FLUENTD_JOURNAL_UPGRADE_DIR="$fixture" \
  go test -mod=readonly -count=1 -timeout=180s -v \
  -run '^TestRegressionJournalUpgradeDiskFixture$' ./internal/controller \
  2>&1 | tee "$evidence/new-reader.log"

git diff --exit-code -- go.mod go.sum
