# Private legacy journal storage

The legacy Fluent/HTTP-event journal now requires an effective-service-UID-owned
root and tag directories with **0700**, and regular retained files with **0600**
and exactly one hard link. This includes data, ACK, recovery and lock files and
any other files within a tag directory. A nested directory, symlink, hard-linked
file, foreign owner or broader mode is refused before that backend creates its
writability probe, lock or segments. No implicit chmod, chown, unlink or payload
rewrite is performed. Startup fails closed on an unsafe retained tag; live
admission completes its acceptance receipt with an error and does not forward
it as durable. Existing accepted records are not ACKed or deleted to make room.

New roots and tags request 0700. The pinned go-journal revision
`v1.1.7-0.20261006223601-a7f7377c4a3d` (upstream PR #12) creates data/ACK/recovery
files as 0600 and no longer changes the process-wide umask in PrepareDir.
There is no create-wide-then-chmod race window. A restrictive umask can further
restrict creation; it is never globally cleared. Deploy with umask 0077 or 0022;
an unusual umask that removes required owner bits may cause a clear refusal.
The effective modes, not just the requested creation flags, are checked on reopen.

## Upgrade and explicit offline migration

**This deliberately changes startup compatibility for existing permissive state.**
Earlier fresh tag directories were already 0700 and protected otherwise readable
files behind them. The security issue was conditional exposure through a
traversable existing tree and permissive files, not disclosure on every fresh
installation. Upstream's 0600 default alone does not make an old wide tree safe.

Before upgrading, stop every writer using the storage. Keep an access-controlled
backup/snapshot of the complete root, including data, ACK and recovery files.
Resolve the configured root deliberately (it may be an operator-managed symlink),
and verify trusted ownership of its parent path and mount. Review the complete
retained layout without following tag/file symlinks. Identify unexpected nested
objects, foreign owners, hard links and ACLs before changing anything. Do not
blindly recurse through links or treat chmod as corruption repair.

After reviewing the tree, explicitly set the intended service owner and 0700 on
the root and each real tag directory, and 0600 on each reviewed regular,
single-link file. Only an operator should authorize these changes while stopped.
Leave uncertain evidence and link targets intact and investigate them separately;
do not delete evidence just to pass startup. The service has no automatic
migration or broader group-sharing switch. Keep the same tag names, all pending
data/ACK records and the same compression configuration. Start one writer, inspect
errors, and verify normal replay/ACK handling before resuming ingress.

For an **empty** demo root owned by the eventual service user:

```sh
mkdir -p -m 700 var/go-fluentd/journal
```

This command does **not** tighten an existing root. A Docker bind mount must have
the correct host ownership for the effective container service UID. Do not use
0777 or a group-sharing mode as a workaround. No on-disk format upgrade is made,
but an older binary can create permissive files again, so a binary rollback is
not a rollback of this security guarantee.

## Trust and operational limits

The existing Linux `/proc/self/fd` directory anchor is retained for rotation and
replay. Root permission/ownership is rechecked before opening a new tag, and the
whole flat child inventory is checked in bounded read batches before using it.
This is startup/admission validation, not continuous monitoring of filesystem
changes. Directory inspection is O(retained file count); it does not retain an
unbounded filename list. A trusted root symlink remains supported.

The root pathname, ancestors, mount and service UID must be trusted. Root or a
competing process with the same service UID can bypass or alter these controls;
this is not same-UID sandboxing, protection from a malicious filesystem, encryption
at rest or an ACL management tool. Review default/extended ACLs and volume access
separately. Use a dedicated UID, constrained mounts, disk quotas and normal
backlog monitoring. The separate OTLP storage implementation is not changed.

## Regression and acceptance coverage

The same baseline-compatible reproductions reject 0755/0775 roots/children and
0664 data/ACK/lock files on the fix, but fail their assertions on the old code.
Plain/gzip creation tests run neutral 0000 and restrictive 0077 umasks in separate
processes; they cover writes, ACKs, rotation and reopening. Historical-file
refusal retains exact bytes, and explicit offline migration preserves one pending
record while excluding its acknowledged peer. Startup and live receipt tests
verify no backend creation, no false durable ACK and no forwarded rejected record.

The Linux permissions workflow separately executes foreign-owner and actual
other-UID access tests with root privileges in an isolated runner. An unrelated
UID and an unauthorized same-group UID must fail both read and write access to a
0600 segment, while a sibling control file proves the subprocess really ran and
could traverse the test path. Ordinary unprivileged suites may skip those two
privileged tests; the dedicated workflow rejects skipped or incomplete results.
No test touches production storage or claims filesystem power-loss certification.
