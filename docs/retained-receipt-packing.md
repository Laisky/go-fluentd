# Retained receipt packing

This change preserves acknowledged originals in the normal logging store while
avoiding one file per accepted envelope. It adds no crawler store, mount, endpoint
or application archive API. The default retains originals and rejects new input
at finite capacity. Optional oldest-discard is not implemented here.

## Format, bounds and recovery

Released receipts are copied with their exact bytes into checksummed framed
parts: target 4 MiB, maximum 32 receipts per batch and 1024 committed parts per
store. A single allowed receipt may exceed the target; existing request, codec,
WAL and aggregate limits still apply. Each checksummed index is at most 16 KiB.
Only the last batch has per-receipt metadata; no unbounded message index exists.

Future packing is funded before ingress ACK. Packing synchronizes part data,
publishes and synchronizes the committed offset and directory, verifies the
committed batch, then removes duplicate loose copies. Partial batches keep their
progress credit across restart. All copies, staging and crash evidence count.

Uncommitted crash tails stay charged and are never promoted or overwritten.
Missing/corrupt authority, unsafe modes/ownership/links, and more than 128 index
stages fail closed. Exhaustion needs attended investigation, not automatic
cleanup or raised limits. Existing 4096-file/entry and 25 ms scan defaults remain.
Packing reduces entry growth but cannot guarantee a production retention horizon.

Generation3 marks retained packed storage. Earlier readers must refuse it.
Rollback after any generation3 writes requires compatible reader, format and
accounting support. Preserve the binary and state; never downgrade metadata,
replace current state with old snapshots, or start an older writer on it.

## Independent monitoring

The existing management `/metrics` route exports fixed label-free series with
prefix `gofluentd_logging_`. Payloads, URLs, destination IDs and arbitrary monitor
callbacks never become labels. Suffixes are:

- `shared_budget_configured`, `shared_budget_known`, `shared_limit_bytes`,
  `shared_charged_bytes`, `shared_reserved_bytes`;
- `pending_upper_bound`, `recovery_ready`, `filesystem_available`,
  `filesystem_available_bytes`, `filesystem_free_inodes`;
- `inventory_files`, `inventory_bytes`, `accepted_archive_packing_pending`,
  `accepted_archive_packed_records`, `accepted_archive_parts`;
- `admission_rejected_total`, `codec_admission_rejected_total`,
  `scan_budget_rejected_total`, `shared_budget_rejected_total`,
  `archive_compaction_deferred_total`.

Scrape externally. Alert on scrape failure, absent expected metrics, unknown
accounting, growing pending work/refusals and bytes/inodes near bounds. Use
rate/increase for counters that reset on restart. Missing data and failed rule
queries are unknown. Keep evaluator/notifier outside the collector process and
monitor their failures too. Metrics alone do not prove real-recipient delivery.
This adds no listener or production scrape/access configuration.

## Recovery and whole-scope acceptance

Use the existing stopped-owner `recover-otlp-archive` dry run before a reviewed
`--execute`. It validates every committed packed/loose original before sending
and never mutates sources. Interrupted execution may send a prefix; retries may
duplicate rows. Queryability and HTTP200 do not authorize source retirement.

The shared ledger controls participating managed roots, not Docker stdout writers,
historical backups or arbitrary host files. Inventory them explicitly. Nonempty
unregistered roots refuse startup; do not replace them with dummy reservations or
relabel active obligations. Arbitrary historical-root adoption is not implemented.
A preserving migration requires a reviewed stop/seal, provenance-aware durable
cold copy and independent recovery validation. Removing any local source needs
separate exact approval. Until all included writers/classes are covered, report
the managed ceiling separately from host-wide usage.

See [the overall runbook](overall-disk-budget.md) for commands and dated history.
The approved old backlog reset was completed; it grants no standing deletion
policy or cleanup permission for other retained roots.
