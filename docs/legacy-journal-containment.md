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

This backend integration requires Linux and a usable `/proc/self/fd`. If that
capability is unavailable, journal admission fails closed. A configured root can
itself be a trusted operator-managed symlink; the child is anchored in its opened
target. The journal root and its contents must remain owned by trusted operators:
this change does not defend against malicious segment contents, bind mounts, or
a local principal authorized to modify the retained tree.

Startup opens the same valid existing child directories, and plain/gzip replay
retains the recorded routing tag and original acknowledgement owner. Invalid
effective tags complete the existing acceptance receipt with an error and are
not forwarded or acknowledged as durable. Symlink entries are left untouched;
they are not followed as retained child journals.

Pending issues remain separate: this change does not cap tag cardinality (#31),
alter existing-directory/segment permission policy (#32), or change OTLP and
multiline budgets. Each admitted child retains one additional directory handle;
the shared journal shutdown worker releases handles after backend shutdown.
