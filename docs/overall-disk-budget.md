# Overall go-fluentd disk budget

Status: source qualification for a Draft release. The collector pins
github.com/Laisky/go-journal v1.1.7-0.20261018152132-3f73b25e0029,
reviewed in https://github.com/Laisky/go-journal/pull/16 on current master.
The existing storage-only Draft47 and production remain unchanged. The published
ab4 qualification image contains storage mapping only, not this feature.

## Scope and configuration

This is a general collector feature. A single configured budget covers ALL
go-fluentd-managed persistent roots: legacy per-tag journals, OTLP WALs,
ACKs, replay outcomes/quarantine, checkpoints/indexes/generation metadata and
temporary recovery/compaction/publication evidence. Legacy and OTLP cannot get
separate copies of the configured total. Multiple collector processes reference
the same backing coordinator directory and lock inode.

~~~yaml
settings:
  disk_budget:
    directory: /shared-go-fluentd-budget
    max_bytes: 3000000000
    segment_bytes: 16777216
~~~

For b1, the approved constraint is exactly **3,000,000,000 bytes aggregate**,
decimal GB, across fresh/backlog and their legacy roots. No b1 expansion.
The general feature permits other deployments to choose a different finite
limit (4 MiB–1 PiB); omitting disk_budget preserves their existing behavior.
The default when the feature is configured is 3,000,000,000 bytes.

The directory and state roots must already exist, be private, separate and
persistent. Every participating container must mount the SAME coordinator
directory. Equal text paths backed by different volumes are not a shared budget.
The controller applies this top-level configuration to both legacy and OTLP.
Conflicting OTLP-specific coordinator/limit values are rejected. Existing
per-pipeline admission limits remain additional limits; they cannot raise
the overall ceiling.

The segment target defaults to 16 MiB (binary units), while b1's total remains
decimal 3 GB. It can be configured from 64 KiB to 64 MiB. Input that cannot fit a
bounded segment is explicitly refused, never split or silently truncated.
Legacy tag writers serialize their data writes; funded size rotation happens
before a segment exceeds its configured conservative record budget.

## Accounting and progress reserves

A cross-process flock and fsynced atomic ledger grant space BEFORE mutation.
Input admission funds the original WAL and bounded future ACK/outcome,
rotation, synchronization and recovery work. Each subsequent mutation consumes
its owner's reservation before using unassigned headroom. Full new admission
does not consume another owner's reserved progress space.

Reconciliation uses the larger of logical size and allocated blocks for each
managed entry, including directories and unknown/temp evidence. Hard-link
names count twice conservatively, even when the filesystem shares their inode.
A fixed bounded allowance covers the ledger, atomic replacement temp and lock.
Framing, block-rounding and directory/publication allowances are charged before
new writes. Consequently usable payload capacity is smaller than the limit.

Budgeted legacy journals avoid speculative preallocation of large empty files.
Transactional serialization staging is memory, not a hidden spill directory.
Recovery hard-link evidence remains counted; it is not an exclusion. The
production-source write audit found the managed journal, outcome and checkpoint
write paths; test/load-fixture outputs are not runtime state.

A process crash before a funded write overcounts. Exclusive root ownership and
quiescent reconciliation refund that overcount before requiring startup
headroom, so an exact-full ledger does not deadlock restart merely to recount.
Pending obligations keep their unused reserves. Unused future-write reservations become generally available after all transport
obligations settle. Retained archive bytes remain charged independently.
Root and coordinator filesystem identity are checked; replacement directories,
uncertain ledger writes, malformed metadata and unknown coordinator evidence
fail closed and make accounting unknown.

The ledger controls its participating ownership domain. Filesystem-global
metadata, unrelated applications, Docker-managed stdout logs and immutable
operator backups outside managed roots need separate host-space/inode
monitoring. They are not silently deleted. Backups must be inventoried and
identified explicitly; an active unresolved queue must not be moved or relabeled
as a backup to evade the cap. External writers or unbudgeted older processes
cannot participate in an enforced budget.

## Lossless acknowledgement, rotation and backpressure

- Full OTLP admission returns HTTP 503 before identity/WAL allocation.
  Reliable legacy input receives an explicit acceptance error and is not
  forwarded as successfully persisted input.
- Legacy and OTLP mutations, ACKs, outcomes, synchronization, rotation and
  cleanup require funded accounting. A full/uncertain write does not become a
  successful acknowledgment. Visible refusal/failure counters remain available.
- Both paths replay fsynced original sealed data rather than copying every
  pending payload on each retry. Unacknowledged source files remain intact.
  Repeated plain/gzip outages do not create new empty data segments or grow
  empty compressed frames.
- The overall-budget mode retains **acknowledged legacy originals**. Normal
  replay skips transport-ACKed records, but never automatically cleans their
  last local copy. OTLP retains the complete fsynced acceptance receipt,
  including original envelope, namespace, record ID and logical destination,
  after checkpointing transport progress. A WAL segment may retire only while
  that durable receipt remains. Terminal/quarantine originals also stay charged.
- VictoriaLogs HTTP 200 or a searchable row is not proof of durable disk commit.
  The current implementation has no stronger sink-commit adapter or archive
  retirement operation: originals remain retained indefinitely. With finite
  capacity this eventually refuses new input; it never overwrites accepted data.
  Do not add an automatic age/size deletion policy to this default.
- New retained OTLP generations require shared-budget policy marker 2 and reject
  older or retention-disabled readers. The current legacy controller refuses a
  budget-owned root without its configured budget. Rollback must preserve
  retention/accounting; an old unbudgeted binary is not a safe rollback.
- No destructive rotation mode is included. The optional replacement direction
  remains unclarified, and no deletion is authorized.

Monitor overallDisk known/limitBytes/chargedBytes/reservedBytes/refused/
writeFailures, and OTLP shared_limit_bytes/shared_charged_bytes/
shared_reserved_bytes/shared_budget_known/wal_segment_bytes and
sharedBudgetRejected. Missing/stale accounting is unknown. Keep independent
health, filesystem bytes/inodes, upstream export_failed and delivery metrics.
Backpressure can still overflow a volatile upstream SDK queue; this feature
does not make that queue durable.

## Existing b1 state and non-destructive transition

Independent metadata-only root measurements at 2026-10-18 13:05 UTC found
3,708,342,272 bytes in the active backlog root plus 552,960 bytes fresh:
about **3.709 GB active state**, already about 709 MB over the new cap.
A fresh independent metadata scan started 2026-10-18 15:19:45 UTC measured
4,264,022,016 bytes backlog plus 552,960 bytes fresh: **4,264,574,976 bytes**,
now **1,264,574,976 bytes above 3 GB**. Available host space was 4,852,563,968
bytes (88% used). These are non-atomic observations, not a quota guarantee.
Subreads are non-atomic. WAL was about 1.845 GB; outcomes and bookkeeping make
up much of the rest. This figure excludes separately retained immutable
historical backups. Their current total was not resampled in this phase.
No backup, WAL, receipt or history was deleted or moved.

Unregistered nonempty or over-budget state is refused, not reset or adopted with
invented reservations. Starting a new empty root does not make the old active
obligations disappear from deployment acceptance. The cap is NOT live yet.

A reviewed transition must preserve all current obligations and their
namespace/destination/ACK provenance. First qualify a read-only recovery plan
and exact eligible-ACK reclamation or durable off-host staging, then review
the concrete runtime operation. VictoriaLogs HTTP 200 is not proof of a durable
archive and cannot justify deleting local accepted work. No b1 resize is an
option. A temporary above-cap old-worker drain is also not implicitly approved.

## Finite release and rollback sequence

1. Review journal Draft16 and the stacked collector Draft together. The actual
   collector go.mod/go.sum pin immutable journal3f73b25; release gates run on
   that real module graph without a local replacement. No other selected
   dependency version changes. Do not merge or remove Draft without parent review.
2. Qualify the existing over-budget transition and inventory operator backups.
   Do not truncate or prune accepted queues to force a successful startup.
3. Build/test a new immutable collector artifact containing overall disk limits.
   The published qualification-native-storage-ab4ac487-20261018 image does not.
4. Obtain parent review before any merge/Draft removal and exact approval for
   runtime coordinator mounts/configuration/migration. No new credentials,
   public security settings or irreversible reclamation are authorized here.
5. Qualify the resulting immutable artifact with all legacy/OTLP fresh/backlog
   roots, full-cap recovery, crash/restart, typed content/query and history.
   Promote latest only after release acceptance. Preserve the prior production
   immutable image and all current state/provenance for reviewed recovery.
6. Rollback must understand current budget ownership and codec/generation
   metadata. An old snapshot is evidence, not permission to overwrite newer
   accepted work or bypass accounting.

## Explicit retained archive recovery

Stop the collector owning this OTLP root using a reviewed operation; do not run
against a live owner or copy mutable roots. With the same private persistent
mounts, existing configuration, configured CA files and approved secret injection:

~~~sh
go-fluentd recover-otlp-archive --config /etc/go-fluentd/settings/settings.yml
go-fluentd recover-otlp-archive --config /etc/go-fluentd/settings/settings.yml --execute
~~~

The first invocation only verifies checksummed retained receipts and counts
envelopes. It sends no data, starts no listener, and initializes no fresh root.
The second validates the whole archive before transport, then replays every
accepted envelope through the existing TLS/auth/codec exporter and frozen
logical destination IDs. It needs no new WAL/receipt budget. Missing peers,
corrupt evidence and live ownership fail closed. Only counts/mode are printed;
payloads, endpoints, headers and credentials are not printed. Archive data and
generation metadata are never deleted or rewritten. A command failure may have
sent a prefix; retry can duplicate rows. No exactly-once guarantee is claimed.

This command currently replays the whole retained accepted OTLP archive.
Operators must assess duplicate/history volume and backend retention/query time
ranges before execution. Ordinary replay still handles pending obligations;
archive replay repairs accepted work after downstream loss. For legacy data,
the closed-child recovery command preserves exact original MessagePack:

~~~sh
go-fluentd recover-legacy-archive --config /etc/go-fluentd/settings/settings.yml \
  --directory /path/to/stopped/legacy/child --source-id b1-sealed-child-v1
go-fluentd recover-legacy-archive --config /etc/go-fluentd/settings/settings.yml \
  --directory /path/to/stopped/legacy/child --source-id b1-sealed-child-v1 --execute
~~~

Use one existing closed child journal at a time. The reader takes the existing
journal lock and retained directory capability without initializing a writer or
altering data/ACK files. Full preflight catches a later malformed/oversized record
before any prefix is transmitted. Each original MessagePack object becomes an
OTLP byte body with supplemental format/source-ID/original-ID/tag attributes.
The original tag, ID, nested values, large integers and bytes remain authoritative
inside the unchanged MessagePack. With native storage codec, decode
otel.record.otlp to the protobuf OTLP wrapper, then decode its byte body as
journal.Data MessagePack. This is an explicit archival recovery representation;
it does not silently reinterpret original types as JSON or claim normal legacy
routing delivery. A receiver HTTP ACK is hop-level acceptance; the source remains
retained on every outcome. TLS, configured CA and bearer authentication use the
same exporter; untrusted certificates fail.

## Concrete transition constraint and pending runtime actions

Keeping the same 4.265 GB of accepted local bytes cannot satisfy a 3 GB physical
bound. The source does not infer a compression ratio or eligible deletion from
metadata, reduce reservations below obligations, exclude an active root by
renaming it, or treat a new empty root as migration completion. At least
1,264,574,976 bytes plus future progress overhead must leave the b1 active
ownership domain or be proven redundantly represented before reclamation.
There is no qualified transition that retains all the same local bytes and
needs neither separate storage nor retirement. No b1 expansion is proposed.

An isolated scaled over-cap fixture proves startup refusal preserves all prior
bytes; a synchronized cold copy includes unknown evidence and remains readable
after transport ACK; corrupt copies fail comparison; neither copy nor refusal
deletes/retire sources or establishes live cap compliance. Separate storage is
required for this preserving route. A consistent second recovery-working copy
may also be required for existing pre-budget OTLP generations; the retained-mode
OTLP CLI deliberately rejects a legacy generation it cannot prove.

The exact future runtime operations needing action-time review are:
1. Coordinate a brief stop of b1 observability-qualification-edge-1 and its private
   edge-backlog companion to seal roots
   /home/laisky/observability-state/task20261017-07-active/{edge-live,edge}.
   Review upstream buffering/outage impact before stopping either process.
2. Provision a protected archive location on existing home storage, proposed
   /home/laisky/observability-state/task20261017-07-archive/b1-active-sealed-v1,
   then copy both entire sealed roots with provenance/unknown evidence,
   synchronize the destination and verify completeness and actual recovery.
   No credentials, security settings, network rules or machines are changed by
   this source work. The proposed directory/copy has NOT been created or run.
3. Review any required legacy-generation replay/migration separately. A cold
   copy is preservation evidence, not proof that all old obligations were
   delivered or that old generations are compatible with the new budget.
4. ONLY after independent destination persistence/recovery verification, request
   specific approval to retire the exact old b1 local copies. This irreversible
   deletion is not authorized here. Retained historical operator backups remain
   separately inventoried and preserved; no active obligations are relabeled.
5. Review exact qualified new image digest, mount a single persistent coordinator
   directory into BOTH workers and apply disk_budget.max_bytes=3000000000 to
   every managed legacy/OTLP root, with archive retention enabled. This changes
   runtime configuration/mounts and may recreate the two collectors; it has not
   been performed. Do not initialize metadata over old unregistered roots.

Existing authorization to publish the exact ab4 storage-only qualification image
was fulfilled: ppcelery/go-fluentd:qualification-native-storage-ab4ac487-20261018
is published at sha256:ccc7100df89279fab95ad6c56db729ca2457da39eed8e17e10b61f8197fb980d.
It is not blocked on the old unanswered question. A materially changed image
containing this feature needs its own exact qualified artifact/publication and
runtime review; latest remains untouched. Source Draft publication does not
authorize deployment, credential generation, public nginx changes or deletion.

Qualification covers process crashes with the original persistent disk intact,
not host/disk destruction or power loss. Fsync guarantees still depend on
filesystem/storage behavior. Upstream SDK queues may drop before collector
acknowledgement; HTTP refusal/failed exports need independent alerting and
appropriate upstream retry/durable handling. No production rollout, latest
promotion, merge or destructive rollback occurred in this work.
