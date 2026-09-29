# Dependency upgrade — 2026-09-28

## Scope and selected versions

Baseline: `69a1da628fa81d5a35771e66ea95d3423a7ccc83`. Go remains `1.27.0` in `go.mod`; verification used Go `1.27.1` on Linux/amd64. Versions were resolved with the Go module proxy and checked against upstream source. This is a compatibility upgrade, not a performance benchmark or a claim that every historical transitive module path has been migrated.

| Dependency | Before | Selected |
| --- | --- | --- |
| go-journal | `v1.1.7-0.20260926040517-fc156a60fafc` | `v1.1.7-0.20260928201541-ad049272dbea` |
| Sarama | `github.com/Shopify/sarama v1.26.4` | `github.com/IBM/sarama v1.61.1` |
| go-utils | `v1.14.6` | `v1.16.0` — compatibility constraint below |
| Laisky/zap | `v1.12.2` | `v1.27.0` |
| Gin / gin-contrib/pprof | `v1.7.0` / `v1.3.0` | `v1.12.0` / `v1.5.6` |
| gin-middlewares | `v1.1.1` | `v1.3.0` |
| Cobra / Viper | `v1.0.0` / `v1.6.3` | `v1.10.2` / `v1.21.0` |
| MessagePack (`msgp`) | `v1.1.9` | `v1.6.4` |
| mapstructure | `mitchellh/mapstructure v1.1.2` | `go-viper/mapstructure/v2 v2.5.0` |
| Application xxhash imports | `cespare/xxhash v1.1.0` | `cespare/xxhash/v2 v2.3.0` |
| Collector pdata | `v1.66.0` | `v1.68.0` |
| x/net | `v0.58.0` | `v0.59.0` |

The journal pin is the merged [upstream PR #10](https://github.com/Laisky/go-journal/pull/10), not an unmerged optimization branch. `go.mod` and `go.sum` contain the complete resolved graph, including compression, protobuf/RPC, telemetry, and HTTP dependencies. The newly introduced `form3tech-oss/jwt-go` indirect dependency is updated to `v3.2.5+incompatible` after resolving the compatible go-utils version.

## Compatibility repairs

### Kafka: replace the obsolete consumer wrapper

Updating Sarama while retaining `go-kafka` / `bsm/sarama-cluster` failed to compile because the legacy offset API no longer matches. The receiver now uses IBM Sarama's native consumer groups; the old wrapper and `KafkaMsg` pool are removed. Both sender and receiver use the same maintained module path. The sender depends on a small application-owned `SendMessages` / `Close` interface rather than forcing test doubles to implement unrelated new Sarama methods.

Configuration keys remain unchanged. New groups retain `OffsetNewest`; existing groups resume their committed offsets. Claim processing preserves per-partition order. Offset marking still happens **after handoff to the acceptor**, not after journal fsync or downstream delivery. Malformed JSON retains the previous discard-and-advance policy; dry mode does not mark offsets. This change does not turn Kafka input into end-to-end exactly-once delivery.

Commit timing is explicit: `interval_sec` controls Sarama's periodic commit, and `interval_num` requests a commit after that many admitted/discarded records **per partition claim**. This replaces the old wrapper's separate commit-filter/one-second broker-commit stages; the exact batching schedule is not identical. Eager range assignment ensures a rebalance cancels claims blocked on downstream backpressure. Retries have a cancellable delay, and the error channel remains drained through client shutdown.

### go-utils: keep the newest verified compatible logger API

`v1.17.0` and `v1.17.1` change logger constructors from `*LoggerType` to `LoggerItf`; the selected go-journal still assigns the result to `*LoggerType`. This is an upstream compilation conflict. Pin `v1.16.0`, with an explanation in `go.mod`, rather than assert an unsupported latest-version combination or add an unsafe type assertion/local fork. Upgrading beyond this requires a corresponding upstream journal logger adaptation and another compatibility run.

The initial unconstrained update also exposed the removed/pre-generics deque constructor. The final compatible graph **does not require deque**, so no deque replacement or version pin remains. `SetupClock` is adapted to `SetInternalClock`, retaining the existing 100 ms refresh interval.

### Configuration and routing

Application imports move to `go-viper/mapstructure/v2` and `cespare/xxhash/v2`. Existing strict OTLP configuration, receiver decoding, sender behavior, and tag-routing contracts run against the selected libraries. Transitive users may still require xxhash v1; this is not a substitute for the application's v2 migration.

## Regression contracts

Kafka tests cover admission-before-marking, malformed records, dry mode, blocked-claim cancellation, connection retry, rebalance re-entry, shutdown error draining, and four concurrent partitions without offset/ID mixing. A separate broker test uses the real application producer and receiver with Apache Kafka `4.3.1`: four partitions, historical records, two consumer lifetimes sharing one group, 64 valid records per lifetime, malformed records, exact committed offsets, and clean shutdown. It repeats three times under the race detector.

Journal tests retain decoded payloads across multiple rotated segments and garbage collection, including binary/Unicode/nested values, a payload larger than 4 MiB, ACK filtering, the maximum ID frontier, plain/gzip files, and two restarts. The cross-version script launches separate processes: the **previous pinned journal writes files**, and the **new journal replays those same files**. Other dependencies are held constant to isolate the journal version change. Existing rejected-payload recovery and post-cleanup ACK-frontier contracts also run.

## Measured acceptance evidence

[Full compatibility run 36480117185](https://github.com/Laisky/go-fluentd/actions/runs/36480117185) tested code commit `57f84ae9b26012cc4114eef8d3466762e379e6d2` after the resolver committed its changes:

| Check | Result |
| --- | --- |
| Module verification, readonly build, vet | Passed |
| Full behavior suite and all component coverage floors | Passed; total statement coverage **71.9%** |
| Full suite, randomized race detector | Passed, **3 repetitions** |
| Focused Kafka/journal upgrade regressions under race detector | Passed, **10 repetitions** |
| Old journal writer → new reader, plain and gzip, repeated reopen | Passed |
| No dependency-file mutation during validation | Passed |

[Real Kafka run 36480545187](https://github.com/Laisky/go-fluentd/actions/runs/36480545187) tested `e3604f0495e953b69d40987a203b523eae1c3cbb`: **all three repetitions passed** (20.22 s, 17.08 s, 16.43 s). The resolved broker image digest was `apache/kafka@sha256:77e3df9054047a88b520d0cc46e16696d3b22022e1d580aeccd2632df6532837`. Expected malformed-record errors in its log are test inputs, not test failures.

These are evidence snapshots before the final JWT patch/test formatting. The PR's final read-only dependency and broker workflow runs provide the corresponding final-head results; they retain logs, resolved module versions, coverage, and cross-version evidence as artifacts for 14 days. The temporary dependency-resolving workflow is replaced by a read-only gate: ongoing CI neither updates versions nor commits code.

## Reproduce

```bash
go mod verify
go build -mod=readonly ./...
go vet -mod=readonly ./...
go test -mod=readonly -count=1 -timeout=180s ./...
go test -mod=readonly -race -count=3 -shuffle=on -timeout=180s ./...
bash .scripts/check_journal_upgrade.sh /tmp/fluentd-journal-compat

go test -mod=readonly -race -count=10 -shuffle=on -timeout=180s \
  -run 'TestRegression(KafkaUpgrade|JournalUpgrade)' \
  ./internal/recvs ./internal/controller

# With a disposable Kafka broker already listening on localhost:9092:
FLUENTD_KAFKA_TEST_BROKERS=127.0.0.1:9092 \
  go test -mod=readonly -race -count=3 -timeout=180s -v \
  -run '^TestRegressionKafkaUpgradeRealBroker$' ./internal/recvs
```

The broker fixture is skipped without its explicit environment variable. The cross-version fixture is invoked by the script, not by an ordinary test run. These tests do not certify every historical journal version, physical power-loss behavior, or Kafka multi-broker/SASL/TLS deployments. No throughput or memory improvement is claimed by this dependency PR.
