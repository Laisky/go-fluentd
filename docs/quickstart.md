# Quickstart and troubleshooting

Follow the [tested root README quickstart](../README.md#quickstart). It builds
from the current checkout and runs HTTP → journal → console without an external
cluster. The [small configuration](settings/quickstart.yml), source build, Docker
command and request example are exercised by the README CI workflow.

The older Compose example under `docs/example/` remains historical material; it
is not the recommended entry point or a claim about current published images.

## What the demo proves

`require_durable_ack: true` makes a successful HTTP response wait for local
journal synchronization. The console sender then logs the event and confirms it
according to its explicit `is_commit: true` setting. This demonstrates the local
processing path, not durable downstream storage or exactly-once delivery.

The routing tag is `demo.sit`: HTTP `tag: demo` appends the process's `--env=sit`.
The payload's `tag` is also `demo.sit` for `/ingest/sit`; it is derived from
`orig_tag` and the URL environment. The console's `demo.{env}` replaces the
placeholder. Other plugins have different naming rules; verify the effective
tag and active environment when adding a destination.

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Build fails immediately | Run `go version`; use Go 1.27 or newer. Build from the repository root using `go build ... .`, not an invented module import path. Dependency downloads need network access. |
| Configuration not found | `--config` points to the checked-in example. Relative journal paths resolve from the process working directory, not from the YAML file's directory. |
| Port 8080 already in use | Stop the other demo. Changing `--addr` also requires changing the request/check URLs. It does not change other receiver addresses. |
| HTTP `400` | Regenerate the timestamp/signature together. The demo allows 300 seconds of age and 30 seconds of clock lead. Use `/ingest/sit`, a JSON object, and the exact demo salt; the signature covers the timestamp, not the payload. |
| HTTP `413` | The demo limits the actual request body to 65,536 bytes. Chunking or omitting Content-Length does not bypass it. |
| HTTP `503` | Check journal permissions, disk availability and pre-persistence filtering. Never treat a rejected/failed response as successful acceptance. |
| Timeout or connection loss | Acceptance may already have happened. Retry with awareness that duplicates are possible; do not delete journal state. |
| `200`, but no `consume msg` line | Wait for downstream processing, then verify `--log-level=info`, console `log_level: info`, `active_env`, and exact output tags. The HTTP response is a local acceptance receipt. |
| Docker permission error | Create the host `var/go-fluentd/journal` directory as the user named by `--user`; check bind-mount ownership. Do not use world-writable state as a workaround. |
| `/health` succeeds during a downstream outage | Expected: this endpoint checks the listener only. Inspect `/monitor`, errors and destination health separately. |

## State and cleanup

The native command and Docker example use the same host `var/go-fluentd/journal`
directory. Never run them simultaneously against it. Stop the application before
any intentional demo-state deletion. Keep the directory across restarts and
upgrades for replay; `docker --rm` removes the container, not the host bind mount.

Do not use cleanup scripts to delete `.buf`, `.ids`, gzip segments or
`.incomplete` evidence as a remedy for a production recovery error. Preserve the
files and investigate using the [reliability notes](reliability.md).

## Verification and next steps

From the repository root, `python3 .scripts/check_readme.py --native` executes the
README commands in an isolated temporary workspace. `--docker` repeats the same
checks with the hardened local-image command. `--static` checks links, anchors,
code fences and required executable blocks without Go or Docker. Both runtime
checks require port 8080 to be free and do not touch an existing demo journal.

For production, replace the console destination with an explicitly validated
backend, protect ingress and diagnostics, use persistent least-privilege state,
and measure with the desired synchronization policy. See
[operations and security](../README.md#operations-and-security),
[delivery qualification](../tests/delivery/README.md), and
[performance measurement](../tests/performance/README.md).
