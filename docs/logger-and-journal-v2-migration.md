# Logger and journal v2 source migration

The collector uses its existing concrete Zap logger directly: `Named`, `With` and `WithOptions` return `*zap.Logger`. No extra concrete SDK factory family, runtime conversion or custom logger wrapper is required.

`library/log.Logger` changes from `*utils.LoggerType` to `*zap.Logger`. External users must update exact type/function assignments and use `log.SetLevel` for the shared dynamic level. Generic SDK alert hooks still attach through the existing Zap hook mechanism. The removed company integration stays removed.

Journal imports move to `github.com/Laisky/go-journal/v2`; this explicitly carries its public logger contract change. Journals receive only the five methods they use. Stored journal bytes, replay funding, retained ACKs and explicit retirement contracts are unchanged.

Current `go-utils/v6/log` already has coherent typed constructors/chains. It was evaluated rather than copied, but importing it into the old v1 utility graph exposes an unrelated generic-deque conflict. This patch therefore does not claim an SDK-wide v6 migration. Existing non-logging v1 utility calls remain separate.

Local replacement qualification is not a released dependency. A normal immutable source revision is pinned only after journal source qualification; no release/tag or deployment is included.
