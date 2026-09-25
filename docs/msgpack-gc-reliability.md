# MessagePack decoded-string lifetime regression

## Finding

PR #17's configured OTLP service acceptance exposed an existing Fluent test
failure in Go CI run `36079794827`: `TestRegressionFluentConcurrentTagRouting`
reported a tag different from the payload's `sourceTag`. Passing reruns alone
were not treated as a fix. The production Fluent sender was left unchanged.

Under Go 1.27.1, two runtime threads and increased GC pressure, an independent
literal-byte oracle reproduced the failure while the captured Forward frame
remained correct. One observed frame was:

```text
92a57461672d3491920081a9736f75726365546167a57461672d34
```

These bytes encode `["tag-4", [[0, {"sourceTag": "tag-4"}]]]`. The generated
MessagePack decoder nevertheless returned `tag-6` for the outer tag. Other
runs returned a missing field or triggered the runtime's `found pointer to free
object` diagnostic. This is a decoder memory-lifetime defect, not evidence that
this sender put the record on the wrong wire route.

## Isolation and correction

The selected `github.com/tinylib/msgp v1.1.2` implements `UnsafeString` by
constructing a standalone `reflect.StringHeader` with a `uintptr` data field.
That integer is not a GC-tracked reference. The conversion is also reached by
normal `Reader.ReadString`, so this is not merely a faulty test assertion.

Two separate diagnostic controls passed 1,000 race-enabled low-GC routing
repetitions each: the dependency's `purego` build, and a temporary copy of the old
dependency changing only `UnsafeString` to a copying `string(b)` conversion.
Another 1,000 repetitions passed with only that function changed to the upstream
fixed conversion. Those temporary replacements were diagnostic experiments,
not the published dependency graph or a production fork.

The correction selects released **msgp v1.1.9**, which removes the standalone
header conversion, together with its required **fwd v1.1.2**. This is a targeted
compatibility update, not a claim that v1.1.9 is the latest MessagePack release.
`go get` and `go mod tidy` generate the committed module files. The selected
before/after module graph changes only these two versions. Tidy also correctly
marks already-used OTLP service dependencies as direct; their versions do not
change. No local replacement or global purego build flag is committed.

References:

- [Original conversion](https://github.com/tinylib/msgp/blob/v1.1.2/msgp/unsafe.go)
- [Corrected released conversion](https://github.com/tinylib/msgp/blob/v1.1.9/msgp/unsafe.go)
- [Go unsafe conversion restrictions](https://pkg.go.dev/unsafe#Pointer)

## Permanent regression protection

The original concurrent tag-routing assertion remains. It now also compares
captured bytes against independent literal MessagePack fixtures before decoding
and compares decoded strings against the known wire tag. Failure output retains
the exact bytes so a decoder defect cannot be mistaken for sender misrouting.
The fixture has positive and deliberately misrouted/trailing-data controls.

The normal Go workflow additionally runs 1,000 repetitions of both routing and
wire-oracle tests under `GOGC=10`, `GOMAXPROCS=2`, and the race detector. Results
are uploaded even on failure. Existing build, vet, coverage, repeated full-suite
race, component, Docker and protocol gates remain in place.

```sh
GOMAXPROCS=2 GOGC=10 go test -mod=readonly -race -count=1000 \
  -timeout=180s -run '^TestRegressionFluent(ConcurrentTagRouting|WireOracleRejectsWrongRoute)$' \
  ./internal/senders
```

GC stress is schedule-dependent; a passing old-version rerun does not invalidate
the retained byte-level failure, and no guaranteed reproduction probability is
claimed. Runtime failures are retained separately from assertion-level failures.
The original hosted failed run remains evidence rather than being erased by a
rerun. Complete native application and protocol checks must pass for the final
revision. This fix makes no throughput, exactly-once or physical-power-loss claim,
and does not by itself complete standalone Collector interoperability acceptance.
