# Logger and journal v2 source migration

The collector uses its existing concrete Zap logger directly: `Named`, `With` and `WithOptions` return `*zap.Logger`. No extra concrete SDK factory family, runtime conversion or custom logger wrapper is required.

`library/log.Logger` changes from `*utils.LoggerType` to `*zap.Logger`. External users must update exact type/function assignments and use `log.SetLevel` for the shared dynamic level. Generic SDK alert hooks still attach through the existing Zap hook mechanism. The removed company integration stays removed.

Journal imports move to `github.com/Laisky/go-journal/v2`; this explicitly carries its public logger contract change. Journals receive only the five methods they use. Stored journal bytes, replay funding, retained ACKs and explicit retirement contracts are unchanged.

Current `go-utils/v6/log` already has coherent typed constructors/chains. It was evaluated rather than copied, but importing it into the old v1 utility graph exposes an unrelated generic-deque conflict. This patch therefore does not claim an SDK-wide v6 migration. Existing non-logging v1 utility calls remain separate.

Local replacement qualification is not a released dependency. A normal immutable source revision is pinned only after journal source qualification; no release/tag or deployment is included.

The concrete logger intentionally preserves the previous v1 SDK's operational emission contract: console output on stdout, RFC3339 timestamps, colour-coded uppercase levels, and no implicit message sampling. This also preserves repeated error events for the opt-in alert hook. The new subprocess regression verifies actual stdout/stderr streams and hook delivery, rather than inspecting a replacement test-only core.

Legacy per-tag journals and the collector-owned OTLP WAL pass named collector loggers through v2 `WithLogger`, sharing normal component log-level policy and configured hooks. The upstream journal still owns separate module-level option/default diagnostics; this patch does not claim to replace that upstream behavior.

The frozen v1 writer → v2 reader tests establish forward replay for the covered plain/gzip fixtures, ACKs, and payload types. They do **not** authorize rollback of v2-mutated or newer retained-generation state into an older binary. Preserve the existing release hold and state/archive recovery requirements until separately qualified.
