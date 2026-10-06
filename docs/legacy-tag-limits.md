# Legacy routing identity admission

`settings.max_tags` is a shared **lifetime** identity ceiling for the configured
legacy pipeline: journal, dump-bypass traffic, dispatcher and producer use one
registry. Zero or omission selects **64**; explicit values must be **1..4096**.
Standalone component constructors also receive a finite default registry unless
the caller supplies a shared `TagBudget`. Embedders constructing several pipelines
must share that registry to enforce one combined ceiling.

The check runs before a new journal directory, backend, buffered channel, worker,
filter pipeline, positive sender cache or unsupported-tag cache is allocated.
Already reserved identities remain usable at saturation. A tag rewrite before the
journal or after tag/post filters cannot bypass the shared ceiling. All registry
keys are nonempty Linux directory components of at most **255 bytes**. Dotted
names, Unicode, spaces, colons and literal backslashes remain supported; the
existing slash/dot/NUL restrictions apply at every stage.

Reservations are not returned when a tag is closed or backend creation fails.
This is deliberate: other stages may still retain caches, workers, or outstanding
records for that identity. Reopening the **same** tag consumes no new reservation.
There is no unsafe LRU eviction, TTL deletion, journal pruning or implicit ACK.
Reducing the configured ceiling requires reviewing retained state and valid tag
inventory before restarting; do not delete unacknowledged data to fit the limit.

At startup the root is inventoried in one bounded read of at most `max_tags + 1`
entries. The entry ceiling includes ordinary files and symlinks as well as child
directories; unexpected junk cannot induce an unbounded scan. An excessive or
invalid inventory fails startup **before opening any child backend**, with all
retained files left intact. Safe child names are reserved atomically and opened
in sorted order. Symlinks are not followed. A later I/O error cancels startup
workers and closes already opened backends/anchors without deleting evidence.

On live saturation, an available local-acceptance receipt is completed with
`ErrLegacyTagLimit`; the message is not forwarded or reported as durable. In the
post-journal dispatcher/producer paths, the durable copy and original
`JournalTag`/ID ownership remain responsible for replay: refusal does **not** enter
the success/commit path. Legacy Fluent traffic without an acceptance protocol
still has its pre-existing best-effort semantics; this change does not invent an
upstream ACK or retry guarantee. Configure a finite tag allowlist or fixed tag
rewrite at ingress where possible.

`reservedTags`, `maxTags`, and `tagAdmissionRejected` are exposed on each protected
journal/dispatcher/producer monitor; because the registry is shared these are the
same aggregate counters, not quantities to add together. Rejected identity values
are not cached or used as metric labels. Saturation does not log per-tag errors.

This caps tag-dependent resource **cardinality**, not total process RSS or journal
bytes. Per-tag channel sizes, segment allocation and backlog still matter. At the
default 10,000/50,000 pointer channel sizes on a 64-bit platform, 64 tags alone can
reserve roughly 30.7 MB of pointer storage, before records, backend state and
workers. Operators setting much larger queues or tag limits must size memory,
file descriptors and dedicated filesystem quotas independently. No production
capacity claim or host qualification follows from these bounded unit tests.
