# JWT dependency review — 2026-09-28

## Finding and scope

The owner supplied Snyk finding `SNYK-GOLANG-GITHUBCOMGOLANGJWTJWT-8341243`,
CVE-2024-51744. The [maintainer advisory](https://github.com/golang-jwt/jwt/security/advisories/GHSA-29wx-vh33-7x7r)
and [Go advisory GO-2024-3250](https://pkg.go.dev/vuln/GO-2024-3250)
identify a dangerous error-handling pattern: an invalid signature and expired
claims can produce combined errors, and a caller that handles only expiry can
miss the signature failure. The v4 fix is 4.5.1. Rejecting every non-nil parse
error avoids accepting a token merely because it is expired.

There is an important identity distinction. The Snyk item names the unversioned
`github.com/golang-jwt/jwt` path. Before this follow-up, the selected application
used `github.com/golang-jwt/jwt/v4 v4.5.2`, already beyond that fix, not an
unversioned v3 module. The report title alone does not demonstrate an exploitable
JWT authentication path in this application.

## Minimal application repair

The only application use of `Laisky/gin-middlewares` was
`BindPrometheus(server)`. The reviewed [v1.3.0 wrapper](https://github.com/Laisky/gin-middlewares/blob/v1.3.0/metrics.go)
only creates `ginprometheus.NewPrometheus("gin")` and calls `Use(server)`.
The application now calls that same metrics library directly, at the same point
in route registration, rather than compiling unrelated authentication helpers.

`go mod tidy` removes `github.com/Laisky/gin-middlewares` and
`github.com/golang-jwt/jwt/v4`, including their checksum entries. The already
selected `github.com/zsais/go-gin-prometheus v1.0.3` becomes a direct dependency;
its version does not change. The metrics-only commit changes no other active
dependency versions. The additional conservative gRPC version selection below
is a separate, explicitly verified change. The adopted go-journal pin remains
`v1.1.7-0.20260928201541-ad049272dbea` throughout.

The measured application/test package graph shrinks from **84 to 82 external
modules**. The full selected module graph and package graph contain neither
the removed middleware nor the unversioned or v4 golang-jwt modules. Metadata-only
v5 requirements elsewhere in the full graph are distinct from active imports.

This does **not** remove all libraries named JWT. `go-utils v1.16.0` still imports
`form3tech-oss/jwt-go v3.2.5+incompatible`; historical module metadata also includes
`dgrijalva/jwt-go`. Those are separately named module paths. This change neither
renames/replaces them to hide them from scanners nor claims to have upgraded them.
The logger API constraint documented in the dependency upgrade report remains.

## Verified behavior and regression detection

[Resolution/verification run 36497220526](https://github.com/Laisky/go-fluentd/actions/runs/36497220526)
verified the tree committed as `8dfacfb2243d1118c3324dfc3c89124900eae3f3`.
[Raw evidence](https://github.com/Laisky/go-fluentd/actions/runs/36497220526/artifacts/11003404078)
includes both package/module graphs, exact manifests, compiler, test JSON and
negative-control output.

- Module verification, build, vet, full behavior tests and tidy consistency pass.
- The identical metrics test passes **three times under the race detector on each
  implementation**: pre-remediation application `a1165c8` and the candidate.
  The test executes real `RunServer` route registration in an isolated process,
  without replacing a registry shared with background tests.
- The HTTP contract checks GET/POST health responses, the pprof goroutine route,
  `/metrics` status/content type, four existing `gin_*` metric families,
  runtime/process collectors, five request-counter labels, body/header/status
  preservation, and exclusion of repeated scrapes from request counters.
- The dependency policy rejects the real previous graph with its intended
  reintroduced-module diagnostic. The old application compiles and passes the
  same HTTP test first: a compilation error is not the negative control.
- Eight Python unit tests exercise the dependency policy, including replacement,
  incomplete/empty data, malformed JSON and reintroduced module/package cases.

The permanent read-only dependency workflow runs this policy and repeats the
metrics regression with journal/Kafka contracts. Both one-time resolving
workflows have been deleted. A separate read-only security workflow uses pinned
`govulncheck v1.8.0` on application/test source and a built production binary. It
uses text mode and fails on either nonzero scanner exit; JSON's success exit on
findings is deliberately not used as an acceptance condition.

## Additional gRPC advisory/source discrepancy

[Initial independent scan 36497638686](https://github.com/Laisky/go-fluentd/actions/runs/36497638686)
at `2cadfd0` returned source exit 0 but binary exit 3. It found
`transport.http2Server.HandleStreams` under [GO-2026-6443](https://pkg.go.dev/vuln/GO-2026-6443)
in `grpc v1.84.0`; it did not find this JWT CVE. Source call-graph analysis did
not find a reachable vulnerable call. These different results are retained,
not collapsed into a claim of demonstrated production exploitation.

The advisory's version range includes 1.84.0 and explicitly marks stable 1.83.2
fixed. However, inspection of [1.84.0 source](https://github.com/grpc/grpc-go/blob/v1.84.0/internal/transport/http2_server.go)
shows that its missing-authority/Host rejection guard is already present, as it
is in [1.83.2](https://github.com/grpc/grpc-go/blob/v1.83.2/internal/transport/http2_server.go).
The published affected range and source therefore require reconciliation.

The conservative selection is **stable 1.83.2**, already used by the original
master and explicitly covered by the published security fix, rather than a
development pseudo-version or a scanner exception. This is not a claim that
1.84.0 was reproduced as exploitable. `go.mod` documents the discrepancy. The
active dependency comparison permits **only grpc 1.84.0 → 1.83.2**, with no
other active-module version or membership changes.

[Final validation 36498544988](https://github.com/Laisky/go-fluentd/actions/runs/36498544988)
passed on the exact candidate committed as **`6ee62a8e6dfd5757dd03b4cfad1ef632637e1d13`**.
[Evidence artifact](https://github.com/Laisky/go-fluentd/actions/runs/36498544988/artifacts/11004227720)
contains final manifests, active-module comparison, full test/race output,
old-writer/new-reader journal results and both scanner reports.

| Final candidate check | Result |
| --- | --- |
| Modules, build, vet and full behavior suite | Passed |
| Full shuffled race suite | Passed, three repetitions |
| Previous journal writes / candidate reads, gzip and plain | Passed |
| Source and production-binary govulncheck | Both exit 0; no symbol-level or package-level findings |
| Dependency policy and immutable manifests | Passed |

Both final scanner reports retain a module-level informational finding for
`golang.org/x/crypto/openpgp` (GO-2026-5932), a package not imported/called by this
build. It is not suppressed. An exit-0 source/binary scan does not mean every
package in every transitive module is free of vulnerabilities.

The earlier attempt 36498039089 ran passing behavior/race and cross-version
cases but failed the script's clean-manifest check because its dependency edit
was not yet committed locally. The corrected run committed a local candidate
before validation and pushed it only after every check passed. The permanent
immutability check was not weakened.

## Snyk result is still a separate acceptance condition

Fresh Snyk checks on `8dfacfb`, `8f07253`, and the final tested code `6ee62a8`
still reported failure. GitHub exposes no package/version/path details in those
statuses. Therefore removal of this unnecessary dependency and the independent
passing scans are **not** described as a completed Snyk remediation. It is also
not established whether the latest failed status still represents the same
JWT finding or a different finding. No check is ignored or overridden.

[Snyk's Go documentation](https://docs.snyk.io/supported-languages/supported-languages-list/go)
explains that default SCM analysis resolves the full module graph, while
full-source analysis and CLI resolution inspect package imports. This is one
possible source of differences, not a verified explanation of this failure.
To reconcile it, inspect the scanned manifest/revision, detected package version
and the finding's **Introduced through** chain. Do not change scanning policy
merely to obtain a green check.

## Reproduction

```bash
python3 .scripts/test_metrics_dependencies.py
python3 .scripts/check_metrics_dependencies.py /tmp/metrics-dependencies
go test -mod=readonly -race -count=3 -timeout=180s \
  -run '^TestRegressionMetricsDependencyCompatibility$' ./internal/controller
go test -mod=readonly -race -count=3 -shuffle=on -timeout=180s ./...
bash .scripts/check_journal_upgrade.sh /tmp/journal-compatibility
go install golang.org/x/vuln/cmd/govulncheck@v1.8.0
govulncheck -show=verbose -test ./...
go build -mod=readonly -o /tmp/go-fluentd-security .
govulncheck -show=verbose -mode=binary /tmp/go-fluentd-security
```

Neither passing behavioral tests nor a clean independent scanner proves absence
of all possible vulnerabilities. No merge or deployment is performed.
