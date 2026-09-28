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
its version does not change. No other dependency version is changed in this
follow-up. The latest adopted go-journal pin is preserved.

The measured application/test package graph shrinks from **84 to 82 external
modules**. The full selected module graph and the package graph contain neither
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
  implementation**: the pre-remediation application at `a1165c8` and the candidate.
  The test executes real `RunServer` route registration in an isolated process,
  without replacing a registry shared with background tests.
- The HTTP contract checks GET/POST health responses, the pprof goroutine route,
  `/metrics` status/content type, the four existing `gin_*` metric families,
  runtime/process collectors, the five request-counter labels, body/header/status
  preservation, and exclusion of repeated scrapes from request counters.
- The dependency policy rejects the real previous graph with its intended
  reintroduced-module diagnostic. The old application compiles and passes the
  same HTTP test first: a compilation error is not the negative control.
- Eight Python unit tests exercise the dependency policy, including replacement,
  incomplete/empty data, malformed JSON and reintroduced module/package cases.

The permanent read-only dependency workflow runs this policy and repeats the
metrics regression with the journal/Kafka contracts. The one-time resolving
workflow has been deleted. A separate read-only security workflow uses pinned
`govulncheck v1.8.0` on application/test source and a built production binary. It
uses text mode and fails on either nonzero scanner exit; JSON's success exit on
findings is deliberately not used as an acceptance condition. Current results
and exact tested revisions belong in the PR description, not implied here.

## Snyk result is still a separate acceptance condition

The fresh Snyk checks on `8dfacfb` and `8f07253` still reported failure after this
removal. Their GitHub status exposes no package/version/path details. Therefore
removal of this unnecessary dependency is **not** described as a completed Snyk
remediation, and the check is not ignored or overridden.

[Snyk's Go documentation](https://docs.snyk.io/supported-languages/supported-languages-list/go)
explains that its default SCM integration resolves the full module graph, while
full-source analysis and CLI resolution inspect package imports. This is one
possible source of differences, not a verified explanation of this particular
failure. To reconcile the remaining check, inspect the scanned manifest/revision,
reported package version and the finding's **Introduced through** chain. Do not
change organization scanning policy merely to obtain a green check.

Reproduction:

```bash
python3 .scripts/test_metrics_dependencies.py
python3 .scripts/check_metrics_dependencies.py /tmp/metrics-dependencies
go test -mod=readonly -race -count=3 -timeout=180s \
  -run '^TestRegressionMetricsDependencyCompatibility$' ./internal/controller
go install golang.org/x/vuln/cmd/govulncheck@v1.8.0
govulncheck -show=verbose -test ./...
go build -mod=readonly -o /tmp/go-fluentd-security .
govulncheck -show=verbose -mode=binary /tmp/go-fluentd-security
```

Neither passing behavioral tests nor a clean independent scanner proves that
all possible vulnerabilities are absent. No merge or deployment is performed.
