# Legacy journal tag containment

Legacy journal admission uses the final effective tag, after receiver rewrites,
as a single Linux directory component. Empty tags, `.`/`..`, slash paths,
absolute paths and NUL bytes are rejected before opening a journal. Dotted tags,
Unicode, spaces, colons, literal backslashes and ordinary single-component names keep their existing
on-disk names. There is no automatic rename, encoding or deletion of retained data.

On Linux, the service creates/opens the child relative to an open parent directory
with `mkdirat` and `openat(O_DIRECTORY|O_NOFOLLOW)`. Existing child symlinks are
rejected. The path-only go-journal backend receives `/proc/self/fd/<child-fd>`;
the service keeps that descriptor alive until the backend closes. Replacing a
child name with a symlink later therefore does not redirect rotation or replay
to the replacement target. Descriptor paths are checked against the opened inode.

Supported deployment is **Linux with procfs mounted and `/proc/self/fd`
accessible to the service**. macOS and other non-Linux deployments are not
supported. Standard Linux container runtimes provide procfs; a chroot or a
container that masks/restricts `/proc` must provide usable descriptor paths.
If that capability is unavailable, journal admission fails closed: the acceptance
receipt contains an error, the event is not forwarded as durable, no backend is
registered, and the temporary child descriptor is closed. An empty tag directory
may already have been created, but no lock or segment state is created there. A configured root can
itself be a trusted operator-managed symlink; the child is anchored in its opened
target. The journal root and its contents must remain owned by trusted operators:
this change does not defend against malicious segment contents, bind mounts, or
a local principal authorized to modify the retained tree.

Startup opens the same valid existing child directories, and plain/gzip replay
retains the recorded routing tag and original acknowledgement owner. Invalid
effective tags complete the existing acceptance receipt with an error and are
not forwarded or acknowledged as durable. Symlink entries are left untouched;
they are not followed as retained child journals.

The shared lifetime tag ceiling is documented in [legacy tag limits](legacy-tag-limits.md).
The [private storage policy](legacy-journal-permissions.md) also checks root/child
ownership, directory modes and retained file types/modes before opening the backend.
OTLP and multiline budgets remain separate. Each admitted child retains one additional directory handle;
the shared journal shutdown worker releases handles after backend shutdown.
