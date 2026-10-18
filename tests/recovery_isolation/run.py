#!/usr/bin/env python3
"""Synthetic-only two-WAL recovery acceptance using the published collector image.

All containers share a disposable network-none namespace; no host publications,
production state, real credentials, daemon faults, or firewall commands.
"""

import argparse
import collections
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import time
import uuid

IMAGE = "ppcelery/go-fluentd@sha256:596e34b01800570c8ae301b5484f84bc354bed49208717ac0ee5c0c2ca9d515b"
TOKEN = "synthetic-recovery-isolation-only"
SIGNALS = ("logs", "metrics", "traces")
SINK_CODE = r"""
import collections,gzip,http.server,json,os,threading,time
from pathlib import Path
lock=threading.Lock();ledger=Path('/test/ledger.jsonl');mode={'block_old':True,'block_all':False,'old_delay':.2}
class Handler(http.server.BaseHTTPRequestHandler):
 def log_message(self,*a):pass
 def answer(self,code,value):
  b=json.dumps(value).encode();self.send_response(code);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(b)));self.end_headers();self.wfile.write(b)
 def do_GET(self):
  if self.path=='/health':return self.answer(200,{})
  if self.path=='/ledger':
   with lock:rows=[json.loads(x) for x in ledger.read_text().splitlines()] if ledger.exists() else []
   return self.answer(200,rows)
  return self.answer(404,{})
 def do_POST(self):
  b=self.rfile.read(int(self.headers.get('Content-Length','0')))
  if self.path=='/control':
   with lock:mode.update(json.loads(b))
   return self.answer(200,{})
  if self.headers.get('Content-Encoding')=='gzip':b=gzip.decompress(b)
  j=json.loads(b);tag=j['futureEnvelope']['test_case'];seq=j['futureEnvelope']['sequence'];signal=self.path.rsplit('/',1)[1]
  with lock:m=mode.copy()
  old=tag=='backlog'
  if old:time.sleep(m['old_delay'])
  if m['block_all'] or (old and m['block_old']):return self.answer(503,{})
  with lock:
   with ledger.open('a') as f:f.write(json.dumps({'case':tag,'sequence':seq,'signal':signal,'sha256':__import__('hashlib').sha256(b).hexdigest()})+'\n');f.flush();os.fsync(f.fileno())
  return self.answer(200,{})
http.server.ThreadingHTTPServer(('127.0.0.1',4318),Handler).serve_forever()
"""


def until(check, message, seconds=40):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        try:
            if check():
                return
        except (subprocess.CalledProcessError, ValueError, KeyError, OSError):
            pass
        time.sleep(0.05)
    raise AssertionError(message)


class Campaign:
    def __init__(self, root):
        self.root = root
        self.label = uuid.uuid4().hex[:10]
        self.names = []
        self.results = []
        self.expected = {}
        self.workers = {}
        self.sink = None

    def command(self, args, **kw):
        return subprocess.check_output(args, **kw)

    def mark(self, name, **evidence):
        row = {"check": name, "passed": True, **evidence}
        self.results.append(row)
        print(json.dumps(row), flush=True)

    def http(self, port, path, body=None, token=False):
        pid = self.command(
            ["docker", "inspect", "--format", "{{.State.Pid}}", self.sink], text=True
        ).strip()
        args = [
            "sudo",
            "-n",
            "nsenter",
            "-t",
            pid,
            "-n",
            "--",
            "curl",
            "--noproxy",
            "*",
            "--silent",
            "--show-error",
            "--max-time",
            "5",
            "-w",
            "\n%{http_code}",
            "http://127.0.0.1:" + str(port) + path,
        ]
        if body is not None:
            args += ["-H", "Content-Type: application/json", "--data-binary", "@-"]
        if token:
            args += ["-H", "Authorization: Bearer " + TOKEN]
        out = self.command(args, input=body, stderr=subprocess.DEVNULL)
        data, status = out.rsplit(b"\n", 1)
        return int(status), data

    def control(self, **values):
        assert self.http(4318, "/control", json.dumps(values).encode())[0] == 200

    def ledger(self):
        status, data = self.http(4318, "/ledger")
        assert status == 200
        return json.loads(data)

    def monitor(self, worker):
        status, data = self.http(self.workers[worker]["management"], "/monitor")
        assert status == 200
        return json.loads(data)

    def generation(self, root):
        return json.loads((root / "otlp/generation.json").read_text())

    def payload(self, case, sequence, signal):
        marker = "isolation-" + case + "-" + str(sequence)
        stamp = str(time.time_ns())
        record = {}
        if signal == "logs":
            record = {
                "resourceLogs": [
                    {
                        "scopeLogs": [
                            {
                                "logRecords": [
                                    {
                                        "timeUnixNano": stamp,
                                        "body": {"stringValue": marker},
                                    }
                                ]
                            }
                        ]
                    }
                ]
            }
        elif signal == "traces":
            record = {
                "resourceSpans": [
                    {
                        "scopeSpans": [
                            {
                                "spans": [
                                    {
                                        "traceId": hashlib.sha256(
                                            marker.encode()
                                        ).hexdigest()[:32],
                                        "spanId": hashlib.sha256(
                                            (marker + "span").encode()
                                        ).hexdigest()[:16],
                                        "name": marker,
                                        "startTimeUnixNano": stamp,
                                        "endTimeUnixNano": str(int(stamp) + 1),
                                    }
                                ]
                            }
                        ]
                    }
                ]
            }
        else:
            record = {
                "resourceMetrics": [
                    {
                        "scopeMetrics": [
                            {
                                "metrics": [
                                    {
                                        "name": "isolation",
                                        "gauge": {
                                            "dataPoints": [
                                                {
                                                    "timeUnixNano": stamp,
                                                    "asInt": str(sequence),
                                                }
                                            ]
                                        },
                                    }
                                ]
                            }
                        ]
                    }
                ]
            }
        record["futureEnvelope"] = {"test_case": case, "sequence": sequence}
        return json.dumps(record, separators=(",", ":")).encode()

    def emit(self, worker, case, sequence, accepted=True):
        for signal in SIGNALS:
            body = self.payload(case, sequence, signal)
            status, _ = self.http(
                self.workers[worker]["listen"], "/v1/" + signal, body, True
            )
            assert status == (200 if accepted else 503), (worker, case, signal, status)
            if accepted:
                self.expected[(case, sequence, signal)] = hashlib.sha256(
                    body
                ).hexdigest()

    def config(self, state, listen):
        return {
            "settings": {
                "otlp": {
                    "enabled": True,
                    "listen_addr": "127.0.0.1:" + str(listen),
                    "storage_dir": "/state/otlp",
                    "bearer_token_env": "ISOLATION_TOKEN",
                    "journal_gzip": True,
                    "max_wire_bytes": 1048576,
                    "max_decoded_bytes": 4194304,
                    "max_items": 10000,
                    "max_response_bytes": 65536,
                    "max_wal_bytes": 1073741824,
                    "max_storage_bytes": 2147483648,
                    "max_storage_files": 4096,
                    "storage_scan_max_entries": 1024,
                    "storage_scan_timeout": "10ms",
                    "receipt_gc": True,
                    "replay_batch": 1,
                    "replay_interval": "100ms",
                    "request_timeout": "10s",
                    "shutdown_timeout": "30s",
                    "destinations": [
                        {
                            "id": "home-observability-v1",
                            **{
                                s + "_endpoint": "http://127.0.0.1:4318/v1/" + s
                                for s in SIGNALS
                            },
                            "gzip": True,
                            "max_attempts": 1,
                            "timeout": "2s",
                        }
                    ],
                },
                "journal": {
                    "buf_dir_path": "/state/legacy",
                    "buf_file_bytes": 1048576,
                    "is_compress": False,
                    "committed_id_sec": 120,
                    "group_commit_max_messages": 1,
                    "journal_out_chan_len": 32,
                    "commit_id_chan_len": 32,
                    "child_data_chan_len": 32,
                    "child_id_chan_len": 32,
                    "gc_inteval_sec": 3600,
                },
            }
        }

    def start_worker(self, key, state, listen, management, wait=True):
        state.mkdir(mode=0o700, parents=True, exist_ok=True)
        # The ordinary executable requires pre-provisioned private roots; it
        # must not silently initialize a missing production state path.
        for child in ("otlp", "legacy"):
            (state / child).mkdir(mode=0o700, exist_ok=True)
        config = self.root / (key + ".json")
        config.write_text(json.dumps(self.config(state, listen)))
        config.chmod(0o600)
        name = "task07-isolation-" + self.label + "-" + key
        args = [
            "docker",
            "run",
            "-d",
            "--name",
            name,
            "--label",
            "task07.isolation=" + self.label,
            "--network",
            "container:" + self.sink,
            "--user",
            str(os.getuid()) + ":" + str(os.getgid()),
            "--read-only",
            "--cap-drop",
            "ALL",
            "--security-opt",
            "no-new-privileges",
            "--memory",
            "256m",
            "--cpus",
            ".5",
            "--pids-limit",
            "64",
            "--env",
            "GOMEMLIMIT=192MiB",
            "--env",
            "ISOLATION_TOKEN=" + TOKEN,
            "--mount",
            "type=bind,src=" + str(config) + ",dst=/config.json,readonly",
            "--mount",
            "type=bind,src=" + str(state) + ",dst=/state",
            "--tmpfs",
            "/tmp:size=16m,mode=1777",
            IMAGE,
            "--config=/config.json",
            "--env=sit",
            "--addr=127.0.0.1:" + str(management),
            "--log-level=error",
        ]
        self.command(args)
        self.names.append(name)
        self.workers[key] = {
            "name": name,
            "state": state,
            "listen": listen,
            "management": management,
        }
        if wait:
            until(
                lambda: self.http(management, "/health")[0] == 200,
                "worker did not start: " + key,
            )
        return name

    def stop(self, key, kill=False):
        name = self.workers[key]["name"]
        self.command(
            ["docker", "kill", "--signal", "KILL", name]
            if kill
            else ["docker", "stop", "--timeout", "15", name]
        )

    def restart(self, key):
        self.command(["docker", "start", self.workers[key]["name"]])
        until(
            lambda: self.http(self.workers[key]["management"], "/health")[0] == 200,
            "restart failed",
        )

    def counts(self):
        return collections.Counter(
            (r["case"], r["sequence"], r["signal"]) for r in self.ledger()
        )

    def all_delivered(self):
        return set(self.counts()) == set(self.expected)

    def run(self):
        assert not self.root.exists()
        self.root.mkdir(mode=0o700, parents=True)
        (self.root / "sink.py").write_text(SINK_CODE)
        self.sink = "task07-isolation-" + self.label + "-sink"
        self.command(
            [
                "docker",
                "run",
                "-d",
                "--name",
                self.sink,
                "--label",
                "task07.isolation=" + self.label,
                "--network",
                "none",
                "--user",
                str(os.getuid()) + ":" + str(os.getgid()),
                "--read-only",
                "--cap-drop",
                "ALL",
                "--security-opt",
                "no-new-privileges",
                "--memory",
                "128m",
                "--cpus",
                ".25",
                "--pids-limit",
                "64",
                "--mount",
                "type=bind,src=" + str(self.root) + ",dst=/test",
                "--env",
                "PYTHONDONTWRITEBYTECODE=1",
                "python@sha256:34386ef0cb081344d7ec1c103ba398e6e9f64e9ab3a1509accc92a4e24a07258",
                "python",
                "/test/sink.py",
            ]
        )
        self.names.append(self.sink)
        until(lambda: self.http(4318, "/health")[0] == 200, "sink did not start")
        seed = self.root / "original-seed"
        self.start_worker("seed", seed, 4319, 8081)
        for i in range(12):
            self.emit("seed", "backlog", i)
        self.stop("seed")
        seed_generation = self.generation(seed)
        active = self.root / "old-active"
        shutil.copytree(seed, active)
        assert not os.path.samefile(seed, active)
        self.start_worker("old", active, 4319, 8081)
        until(
            lambda: not self.monitor("old")["otlpStorage"]["capacity"][
                "recovery_ready"
            ],
            "backlog never entered cold recovery",
        )
        self.emit("old", "refused-single-owner", 0, False)
        assert not self.monitor("old")["otlpStorage"]["capacity"]["recovery_ready"]
        self.mark(
            "Single recovered owner reproduces fresh HTTP503 during bounded first replay pass"
        )
        live = self.root / "live-active"
        self.start_worker("live", live, 4320, 8082)
        live_generation = self.generation(live)
        assert live_generation["namespace"] != seed_generation["namespace"]
        self.control(block_old=False)
        self.emit("live", "fresh-overlap", 1)
        until(
            lambda: all(
                self.counts().get(("fresh-overlap", 1, s)) == 1 for s in SIGNALS
            ),
            "fresh data did not reach sink while old replay runs",
        )
        until(
            lambda: any(x["case"] == "backlog" for x in self.ledger()),
            "backlog did not progress",
        )
        assert not self.monitor("old")["otlpStorage"]["capacity"]["recovery_ready"]
        self.mark(
            "Fresh all-signal admission and delivery continue while isolated historical WAL drains"
        )
        collision = self.start_worker("collision", active, 4330, 8083, False)
        until(
            lambda: self.command(
                ["docker", "inspect", "--format", "{{.State.Status}}", collision],
                text=True,
            ).strip()
            == "exited",
            "second WAL owner did not fail",
        )
        exitcode = int(
            self.command(
                ["docker", "inspect", "--format", "{{.State.ExitCode}}", collision],
                text=True,
            )
        )
        assert exitcode != 0
        logs = self.command(
            ["docker", "logs", collision], stderr=subprocess.STDOUT
        ).decode()
        (self.root / "ownership-rejection.log").write_text(logs)
        assert any(
            term in logs.lower() for term in ["ownership", "already owned", "lock"]
        ), "failure is not proven to be ownership rejection"
        self.mark(
            "Second owner of the same WAL refuses before accepting requests",
            exit_code=exitcode,
        )
        # A synthetic sparse file proves bounded admission pressure without filling disk.
        pressure = live / "otlp" / "synthetic-pressure"
        f = pressure.open("wb")
        f.truncate(2147483648)
        f.close()
        self.emit("live", "pressure-refused", 0, False)
        assert pressure.stat().st_size == 2147483648
        pressure.unlink()
        self.emit("live", "fresh-after-pressure", 2)
        until(
            lambda: self.monitor("live")["otlp"]["acceptedEnvelopes"] == 6,
            "pre-crash fresh outcomes not yet durably resolved",
        )
        self.mark(
            "Root-capacity refusal preserves accepted work and recovers after disposable pressure removal"
        )
        # Pause before killing so known accepted outcomes are durable and the next
        # failed attempt has completed. This is not an ambiguous-ACK dedup claim.
        self.control(block_all=True, block_old=True)
        before = self.monitor("old")["otlp"]["retryableEnvelopes"]
        until(
            lambda: self.monitor("old")["otlp"]["retryableEnvelopes"] > before,
            "old replay did not observe controlled outage",
        )
        self.emit("live", "fresh-crash", 3)
        self.stop("live", True)
        self.stop("old", True)
        before_ledger = self.counts()
        self.restart("old")
        self.restart("live")
        assert self.generation(active)["namespace"] == seed_generation["namespace"]
        assert self.generation(live)["namespace"] == live_generation["namespace"]
        self.control(block_all=False, block_old=False, old_delay=0.01)
        until(
            self.all_delivered, "accepted data missing after both process crashes", 80
        )
        until(
            lambda: self.monitor("old")["otlpStorage"]["capacity"]["recovery_ready"]
            and self.monitor("old")["otlpStorage"]["capacity"]["pending_upper_bound"]
            == 0,
            "old checkpoint did not converge",
            40,
        )
        until(
            lambda: self.monitor("live")["otlpStorage"]["capacity"][
                "pending_upper_bound"
            ]
            == 0,
            "live checkpoint did not converge",
            40,
        )
        counts = self.counts()
        assert all(v == 1 for v in counts.values()), "known accepted outcome duplicated"
        assert all(
            (r["case"], r["sequence"], r["signal"]) in self.expected
            and self.expected[(r["case"], r["sequence"], r["signal"])] == r["sha256"]
            for r in self.ledger()
        ), "payload identity changed"
        self.mark(
            "Both SIGKILL/restarts retain every accepted signal and known-outcome identity without extra rows",
            accepted_keys=len(self.expected),
            row_counts=sorted(set(counts.values())),
        )
        assert self.generation(active)["namespace"] == seed_generation["namespace"]
        assert self.generation(seed) == seed_generation
        self.mark(
            "Historical and live generation/checkpoint continuity preserved; original synthetic seed unchanged",
            old_released=self.generation(active).get("released_through"),
            live_released=self.generation(live).get("released_through"),
        )
        workers = [self.workers[k]["name"] for k in ("old", "live")]
        values = [
            json.loads(
                self.command(
                    [
                        "docker",
                        "inspect",
                        "--format",
                        '{"memory":{{json .HostConfig.Memory}},"cpu":{{json .HostConfig.NanoCpus}},"pids":{{json .HostConfig.PidsLimit}},"ports":{{json .HostConfig.PortBindings}},"oom":{{json .State.OOMKilled}}}',
                        n,
                    ],
                    text=True,
                )
            )
            for n in workers
        ]
        assert (
            sum(x["memory"] for x in values) == 512 * 1024 * 1024
            and sum(x["cpu"] for x in values) == 1000000000
            and sum(x["pids"] for x in values) == 128
            and all(not x["ports"] and not x["oom"] for x in values)
        )
        self.mark(
            "Actual two-worker aggregate caps enforced without host publications",
            memory_bytes=sum(x["memory"] for x in values),
            nano_cpus=sum(x["cpu"] for x in values),
            pids=sum(x["pids"] for x in values),
        )
        self.stop("old")
        self.emit("live", "fresh-after-convergence", 4)
        until(self.all_delivered, "single remaining worker did not accept/deliver")
        self.stop("live", True)
        self.restart("live")
        until(
            lambda: self.monitor("live")["otlpStorage"]["capacity"]["recovery_ready"],
            "single owner did not recover after convergence",
        )
        assert all(v == 1 for v in self.counts().values())
        self.mark(
            "Converges safely to one live worker after historical checkpoint drain; retired WAL retained"
        )
        result = {
            "passed": True,
            "checks": self.results,
            "image": IMAGE,
            "accepted_keys": len(self.expected),
            "sink_rows": len(self.ledger()),
            "limits": [
                "Synthetic isolated processes and controlled outages; no production mutations",
                "Known durable outcomes tested; ambiguous network/ACK outcomes can still duplicate",
                "Loopback-only test namespace; production TLS separately verified and unchanged",
                "Storage thresholds remain logical admission budgets, not filesystem quotas",
                "Live split topology NOT applied by this campaign",
            ],
        }
        (self.root / "summary.json").write_text(json.dumps(result, indent=2) + "\n")
        return result

    def cleanup(self):
        for name in reversed(self.names):
            label = self.command(
                [
                    "docker",
                    "inspect",
                    "--format",
                    '{{index .Config.Labels "task07.isolation"}}',
                    name,
                ],
                text=True,
            ).strip()
            assert label == self.label
            log = self.command(["docker", "logs", name], stderr=subprocess.STDOUT)
            (self.root / (name + ".log")).write_bytes(log)
            self.command(["docker", "rm", "--force", name])


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    os.umask(0o077)
    campaign = Campaign(Path(args.output).resolve())
    try:
        result = campaign.run()
        print(json.dumps({"passed": True, "checks": len(result["checks"])}), flush=True)
    finally:
        campaign.cleanup()
