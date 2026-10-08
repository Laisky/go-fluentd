# CI testing policy (2026-10-08)

At the maintainer's request, automatic pre-merge testing is formatting plus the
fastest essential unit tests. This policy supersedes earlier descriptions of
permanent automatic coverage, repeated race, benchmark, integration, stress,
fuzz, Docker, process-crash, and environment qualification gates in historical
reports. It changes scheduling, not test assertions, production behavior, or
qualification thresholds. Full testing remains the developer's responsibility
on local dev/staging before promoting relevant changes.

## Automatic check

`Go / test` runs Go 1.27.1, gofmt on every tracked Go source, and the exact named
test list in `.scripts/fast_ci_tests.json`. It covers MessagePack framing limits and NDJSON/CloudEvents decoding and ownership.
The unit tests execute once (`-count=1`), with no race, coverage, benchmark, fuzz,
external service or environment campaign. This deliberately limited gate does
not establish whole-application correctness or production readiness.

Discovery must return every configured name, each test must run and pass exactly
once, and the package must finish successfully. Skipped subtests, failures,
missing tests, invalid JSON, native command errors and timeouts fail the gate.
`receipt.json` records actual exits and wall time; raw discovery and Go JSON are
retained on failures as well as success. Build/dependency caching is enabled,
but test-result caching is disabled. A five-minute job timeout bounds runaway
CI; measured runtimes, including cold compilation, are recorded in the PR.

```sh
python3 -m unittest discover -s .scripts -p test_fast_ci.py -v
python3 .scripts/fast_ci.py --evidence /tmp/fast-ci
python3 .scripts/check_readme.py --self-test
python3 .scripts/check_readme.py --static
```

## Retained manual qualification

The original complete Go workflow is preserved as `go-full.yml`, with all build,
vet, full-suite, race/repetition and other steps and thresholds intact. All of
these additional test workflows keep their jobs and assertions and now run only
through **Actions > workflow > Run workflow**, selecting the candidate branch:

- `go-full.yml`
- `combined-formats.yml`
- `delivery.yml`
- `dependency-upgrade.yml`
- `e2e-load.yml`
- `event-formats.yml`
- `group-commit.yml`
- `journal-adoption.yml`
- `kafka-compatibility.yml`
- `otlp-collector.yml`
- `otlp-edge.yml`
- `otlp-http.yml`
- `otlp-journal.yml`
- `otlp-service.yml`
- `otlp-state.yml`
- `otlp-wire.yml`
- `performance.yml`
- `readme.yml` (complete native/Docker integration matrix)

Already-manual historical workflows remain available. The benchmark regression
workflow accepts an explicit rolling baseline ref; use the candidate PR's base
SHA to reproduce its prior comparison. Its pinned policy reference and failure
thresholds stay intact.

For local dev/staging (Go 1.27.1; Linux for OS-specific tests):

```sh
go mod verify
go build -mod=readonly ./...
go vet -mod=readonly ./...
go test -mod=readonly -count=1 -timeout=180s ./...
go test -mod=readonly -race -count=5 -shuffle=on -timeout=180s ./...
go test -mod=readonly -race -count=10 -shuffle=on -timeout=180s -run 'Test(Regression|Recovery|Component)' ./...
git diff --exit-code -- go.mod go.sum
```

Manual workflow YAML and the existing campaign README/docs contain complete
commands, environment requirements, provenance and independent evidence audits.
Keep those assertions intact when running locally; a command error is not a
behavioral reproduction. Production health/failure handling, publishing,
deployment, credentials, secrets, branch protection and repository security
settings are outside this scheduling amendment.

CodeQL, dependency-security and legacy-journal-permissions remain automatic.
Their runtime is separate from the fast Go testing budget. After coordination
with the documentation owner, the README native/Docker integration matrix is
manual; every original job step, assertion, timeout and artifact remains intact.
Its existing checker self-tests and static Markdown/link/shell-syntax checks
remain automatic in `Go / test`. README.md and the checker implementation are
unchanged. Manual examples can also be run with:

```sh
python3 .scripts/check_readme.py --native
python3 .scripts/check_readme.py --docker
```
