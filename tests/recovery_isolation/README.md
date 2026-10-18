# Retained backlog recovery isolation

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
No state trees are merged when converging back to one worker.

## Acceptance

`run.py` starts only disposable containers sharing a network-none namespace with
an independent fsynced HTTP sink. No host port, production state, real credential,
daemon fault or firewall command is used. Both collector containers are capped at
256 MiB, 0.5 CPU and 64 PIDs; their aggregate matches one 512 MiB / one CPU /
128-PID collector. The independent synthetic sink is separate test overhead.

The executable must first reproduce HTTP503 for fresh work at the single cold
owner. A second independent WAL must accept and deliver logs, metrics and traces
while the historical owner makes progress. A competing owner of the same WAL
must fail for the ownership lock, with different unused listener ports so a port
collision cannot pass the assertion. Disposable sparse-file pressure must refuse
new admission without losing accepted work. Actual SIGKILL/restart of both owners
must preserve accepted payload identity, namespaces, checkpoints and known
accepted outcomes. The historical owner is retired only after its pending bound
is zero and its checkpoint is complete; the live owner must continue after that
convergence and another restart. Original synthetic seed state remains retained.

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

The pull-request workflow runs the same exact executable and oracle. A startup,
HTTP, ownership, pressure, restart or ledger failure is a failed qualification.
Compose rendering alone is insufficient. No production rollout is performed by
this workflow or test. Any operational cutover must preserve all old WALs and
backups, one owner per root, existing public bindings/authentication, a measured
resource budget and a documented convergence/rollback decision.
