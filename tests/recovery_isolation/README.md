# Retained backlog recovery isolation

This is an explicit historical-image/default-compatibility control. Its pinned
process image is independent of the checked-out Go source. The Go regression
compiles current source, but this process campaign does not enable the shared
disk budget, retained-original mode or native log-storage codec. It does not
qualify current native storage, a 3GB aggregate cap or production migration.

A restored WAL can be larger than current admission budgets. The ordinary owner
correctly refuses fresh ingress until its retained snapshot is recovered and
physical WAL capacity is reclaimed. `recovery_ready` describes reservation
recovery, not guaranteed free storage. Removing that gate alone cannot make an
over-budget WAL safe, and raising limits without disk/receipt headroom is not a
recovery plan. Accepted old data must never be removed to make a check green.

This campaign qualifies a temporary alternative using the existing published
executable: one private owner drains the retained WAL while the normal ingress
owner uses a separately provisioned bounded WAL. Each original namespace and
checkpoint remains intact. This is a second queue, not a reset of the old queue.
No state trees are merged when converging back to one worker. The historical
checkpoint must release **every accepted historical record**; a zero pending
inventory alone is not sufficient proof of completed recovery.

## Acceptance

`run.py` starts only disposable containers sharing a network-none namespace with
an independent fsynced HTTP sink. No host port, production state, real credential,
daemon fault or firewall command is used. Both collector containers are capped at
256 MiB, 0.5 CPU and 64 PIDs; their aggregate matches one 512 MiB / one CPU /
128-PID collector. The independent synthetic sink is separate test overhead.

The executable must first reproduce HTTP503 for fresh work at the single cold
owner. A second independent WAL must accept and deliver logs, metrics and traces
while the historical owner makes progress. The HTTP 503 capacity controls also require a matching increment in the
storage-admission rejection counter, so unrelated 503 errors cannot pass.
A competing owner of the same WAL
must fail for the ownership lock, with different unused listener ports so a port
collision cannot pass the assertion. Disposable sparse-file pressure must refuse
new admission without losing accepted work. Actual SIGKILL/restart of both owners
must preserve all accepted payload hashes, namespaces, checkpoints and known
accepted outcomes. The historical owner is retired only after its pending bound
is zero and its checkpoint is complete; the live owner must continue after that
convergence and another restart. Original synthetic seed state remains retained;
the assertion checks its generation metadata, not a byte manifest of the entire
seed tree.

Before the final known-outcome SIGKILL, the collector must report zero pending
work and a released checkpoint matching every admitted live envelope. Sink row
presence alone is insufficient. After restart, a fresh three-signal sentinel is
admitted into the same serial replay worker and must reach both the sink and a new
released checkpoint. That barrier proves the worker traversed retained replay
before the final ledger audit; a checkpoint already marked settled at Open is
not enough. The final snapshot checks all 51 keys, exact multiplicities and
payload hashes, including the post-restart sentinel. Negative controls reject
missing, unexpected, duplicate and corrupt records, delayed duplicate replay,
missing sentinels, and unsettled/unknown checkpoints. The test's cleanup always
attempts removal of positively identified owned containers, even when log
collection fails; it never removes containers with unverified ownership.
A green `summary.json` is written **only after cleanup succeeds**. A failed
cleanup cannot leave a passing summary artifact.

The test intentionally pauses downstream work before controlled crashes. It does
not claim exactly-once transport: a crash between remote acceptance and local
receipt persistence can still duplicate a record. Operational queries must not
interpret raw log row counts as authoritative billing or transaction counts.
Thresholds are logical admission budgets, not filesystem quotas. Independent
filesystem headroom and supervision remain necessary during historical drain.
Production TLS/authentication must remain unchanged; the test uses loopback-only
synthetic transport and does not certify real access policy or WAN capacity.

## Run

Use a disposable Linux Docker host with Python 3, sudo/nsenter/curl and the pinned
images. The output directory must not exist. Do not run against real state.

```sh
python3 tests/recovery_isolation/run.py --output /tmp/otlp-recovery-isolation
```

The manually dispatched workflow runs the exact published executable and full
Docker campaign. Only the inexpensive Python oracle controls and two selected
Go recovery tests run automatically. The fast PR gate remains Docker-free. A startup, HTTP, ownership, pressure,
restart or ledger failure is a failed qualification.
Compose rendering alone is insufficient. No production rollout is performed by
this workflow or test. Any operational cutover must preserve all old WALs and
backups, one owner per root, existing public bindings/authentication, a measured
resource budget and a documented convergence/rollback decision.
