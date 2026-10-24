#!/usr/bin/env bash
# A real frozen v1 writer produces disk state; the native v2 controller reads it.
set -euo pipefail
cd "$(dirname "$0")/.."
previous='v1.1.7-0.20260926040517-fc156a60fafc'
if [ "$#" -gt 0 ]; then evidence="$1"; else evidence="$(mktemp -d)"; fi
mkdir -p "$evidence"
evidence="$(cd "$evidence" && pwd)"
# Retain synthetic state and provenance with evidence, including failures.
work="$(mktemp -d "$evidence/fixture.XXXXXX")"
fixture="$work/records"
producer="$work/producer"
mkdir -p "$fixture" "$producer"
cp .scripts/fixtures/journal_v1_writer.go "$producer/main.go"
cat > "$producer/go.mod" <<EOF
module example.invalid/journal-v1-fixture

go 1.27.0

require github.com/Laisky/go-journal $previous
EOF
(
  cd "$producer"
  go mod tidy
  go list -m -json github.com/Laisky/go-journal > "$evidence/previous-version.json"
  go list -m -json all > "$evidence/producer-modules.json"
  go mod verify
  go build -mod=readonly -o "$evidence/v1-writer" .
)
go version -m "$evidence/v1-writer" > "$evidence/producer-build.txt"
"$evidence/v1-writer" "$fixture" "$previous" "$evidence/writer-provenance.json" 2>&1 | tee "$evidence/old-writer.log"
go list -mod=readonly -m -json github.com/Laisky/go-journal/v2 > "$evidence/candidate-version.json"
python3 - "$evidence" <<'PY'
import json, pathlib, sys
root=pathlib.Path(sys.argv[1])
old=json.loads((root/"writer-provenance.json").read_text())
new=json.loads((root/"candidate-version.json").read_text())
if old["module"]!="github.com/Laisky/go-journal" or new["Path"]!="github.com/Laisky/go-journal/v2" or new.get("Replace") is not None:
    raise SystemExit("require a real v1 producer and immutable v2 reader")
PY
FLUENTD_JOURNAL_UPGRADE_MODE=check FLUENTD_JOURNAL_UPGRADE_DIR="$fixture" \
  go test -mod=readonly -count=1 -timeout=180s -v \
  -run '^TestRegressionJournalUpgradeDiskFixture$' ./internal/controller \
  2>&1 | tee "$evidence/new-reader.log"
git diff --exit-code -- go.mod go.sum
