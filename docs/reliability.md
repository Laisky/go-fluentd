# Go 1.27 reliability changes

## Build and verification

Go 1.27 or newer is required. CI selects the latest 1.27 patch; the checked-in Docker images use Go 1.27.1.

```sh
go mod verify
go build -mod=readonly ./...
go vet -mod=readonly ./...
go test -mod=readonly -count=1 -timeout=180s ./...
go test -mod=readonly -race -count=1 -timeout=180s ./...
go test -mod=readonly -race -count=10 -timeout=180s -run TestRegression ./...
docker build -f .docker/Dockerfile -t go-fluentd .
docker run --rm go-fluentd --help
```

The `TestRegression` cases cover pooled batch contents, complete gzip requests, response closure, Elasticsearch bulk failures and metadata, TCP flush/partial-write errors, concurrent tag routing, cancellation, closed-input draining, actual HTTP body-size limits, acknowledgement retries, and durable replay ordering. Successful delivery, HTTP 503 handling, acknowledged-message suppression, and non-destructive acknowledgement lookup remain passing controls.

The journal dependency is pinned to the reviewed Go 1.27 repair commit from [go-journal PR #1](https://github.com/Laisky/go-journal/pull/1). Merge that companion before this application's PR. A future tagged journal release can replace the pseudo-version after running the same checks; do not downgrade to v1.1.6, which lacks the reclamation barrier and error fixes.

## Reliability contract

A replayed record is written to the active journal before its in-memory delivery is published. The journal synchronizes replacement data, IDs, and directory state before deleting consumed legacy files. An unreadable segment is an error, not successful consumption. These checks exercise real temporary files and injected write failures; they are not host-power-loss or storage-hardware qualification.

A Fluent TCP send is successful only after both encoding and flushing succeed. A partial write invalidates the connection, so retries use a new connection. This is still **not** a Fluent Forward application-level ACK, a downstream durable-store receipt, or exactly-once delivery. Retries after an ambiguous network failure can duplicate messages. Elasticsearch partial failures reject the batch rather than silently acknowledge rejected records; retrying that batch can duplicate successful items.

Existing explicit lossy modes (`DiscardWhenBlocked`, dry runs, and busy-journal bypass/discard paths) are not changed into a no-loss guarantee. Operators requiring strict durability need an end-to-end acknowledgement/backpressure policy in addition to these bug fixes.

The journal's two-generation committed-ID cache retains its non-destructive lookup semantics: multiple replay copies may have the same ID. TTL rotation remains separate from the durable reclamation barrier. This change does not replace the cache with a heap/time wheel, remove forced GC based on unmeasured assumptions, or claim a throughput improvement.

## Legacy image boundary

The normal runtime and test Dockerfiles are self-contained. `forward.Dockerfile` retains the deployment-owned MooseFS runtime and requires an externally supplied `startApp.sh`; that script is not in this repository. CI builds its `gobin` stage only. `golang-stretch.Dockerfile` keeps its historical filename for compatibility but now builds on Go 1.27.1 / Debian Bookworm.


## Reliable HTTP acceptance and executable-level qualification

HTTP receiver plugins may enable `require_durable_ack: true`. In that mode a
successful response waits for a completed local journal write and `Sync()`;
pre-persistence queues backpressure instead of bypassing the journal, and write
errors or pre-persistence filter rejection return 503. The default remains false
for compatibility. A client timeout has an unknown outcome and retries may
produce duplicates. This is local durable ownership, not an immediate downstream
ACK, and it has an intentional sync cost.

See [the external delivery contracts](../tests/delivery/README.md) for actual
executable/SIGKILL/restart tests, external ledgers, seeded fault schedules and
old-binary/new-binary controls. The journal reserves IDs from retained data as
well as ACK files and includes the newest sealed segment in first-pass recovery.
Interrupted newest appends retain `.incomplete` evidence rather than erasing
source bytes. Arbitrary corruption still fails closed; physical power-loss,
real-cluster and exactly-once guarantees are not inferred.
