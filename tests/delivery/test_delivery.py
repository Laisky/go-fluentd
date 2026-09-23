"""Black-box delivery tests: no go-fluentd imports or internal success counters.

Run with GO_FLUENTD_BINARY=/absolute/path/to/binary python3 -m unittest discover
-s tests/delivery -v. The binary must be built from the source under test.
"""
from __future__ import annotations

import concurrent.futures
import gzip
import hashlib
import http.client
import json
import os
from pathlib import Path
import random
import socket
import struct
import subprocess
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

SALT = "delivery-test-not-a-secret"
SEED = int(os.environ.get("DELIVERY_SEED", "127"))
DEADLINE = float(os.environ.get("DELIVERY_DEADLINE", "15"))


def wait_for(predicate, detail, timeout=DEADLINE):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        if predicate():
            return
        time.sleep(0.025)
    raise AssertionError(detail() if callable(detail) else detail)


def request(port, method, path, body=None, timeout=5):
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=timeout)
    try:
        conn.request(method, path, body, {"Content-Type": "application/json"})
        response = conn.getresponse()
        return response.status, response.read()
    finally:
        conn.close()


def pack(value):
    """Minimal independent MessagePack producer, not a round-trip app codec."""
    if isinstance(value, str):
        data = value.encode("utf8")
        n = len(data)
        header = bytes([0xa0 | n]) if n < 32 else (b"\xd9" + bytes([n]) if n < 256 else b"\xda" + struct.pack(">H", n))
        return header + data
    if isinstance(value, bytes):
        return b"\xc5" + struct.pack(">H", len(value)) + value
    if isinstance(value, int):
        return bytes([value]) if 0 <= value < 128 else b"\xce" + struct.pack(">I", value)
    if isinstance(value, list):
        n = len(value)
        return (bytes([0x90 | n]) if n < 16 else b"\xdc" + struct.pack(">H", n)) + b"".join(pack(x) for x in value)
    if isinstance(value, dict):
        if len(value) >= 16:
            raise ValueError("fixture map too large")
        return bytes([0x80 | len(value)]) + b"".join(pack(k) + pack(v) for k, v in value.items())
    raise TypeError(type(value))


class Ledger:
    """A separate fsynced sink, never populated from the producer's expectations."""
    def __init__(self, root: Path, name: str):
        self.path = root / (name + ".jsonl")
        self.lock = threading.Lock()
        self.policy = "accept"
        self.attempts = []
        self.received = []
        self.errors = []
        self.release = threading.Event()
        owner = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *_):
                pass

            def do_POST(self):
                try:
                    raw = self.rfile.read(int(self.headers["Content-Length"]))
                    if self.headers.get("Content-Encoding") == "gzip":
                        raw = gzip.decompress(raw)
                    lines = raw.splitlines()
                    if self.path != "/_bulk" or len(lines) % 2 or not lines:
                        raise ValueError("invalid bulk framing")
                    docs = []
                    for i in range(0, len(lines), 2):
                        metadata, doc = json.loads(lines[i]), json.loads(lines[i + 1])
                        if metadata.get("index", {}).get("_index") != "delivery":
                            raise ValueError("wrong destination index")
                        docs.append(doc)
                    with owner.lock:
                        owner.attempts.extend(docs)
                        policy = owner.policy
                    if policy == "hold":
                        owner.release.wait(DEADLINE * 2)
                    accepted = [d for d in docs if policy not in ("reject", "hold", "malformed")
                                and (policy != "new-only" or d["event"].startswith("new-"))]
                    if accepted:
                        with owner.lock, owner.path.open("a", encoding="utf8") as output:
                            for doc in accepted:
                                output.write(json.dumps(doc, ensure_ascii=False, sort_keys=True) + "\n")
                            output.flush()
                            os.fsync(output.fileno())
                            owner.received.extend(accepted)
                    if policy == "lose-ack":
                        self.connection.shutdown(socket.SHUT_RDWR)
                        self.connection.close()
                        self.close_connection = True
                        return
                    # HTTP 200 is deliberately not sufficient: per-item errors matter.
                    items = [{"index": {"status": 201 if d in accepted else 503}} for d in docs]
                    body = (b'{}' if policy == "malformed" else
                            json.dumps({"errors": len(accepted) != len(docs), "items": items}).encode())
                    self.send_response(200)
                    self.send_header("Content-Type", "application/json")
                    self.send_header("Content-Length", str(len(body)))
                    self.end_headers()
                    self.wfile.write(body)
                except (BrokenPipeError, ConnectionResetError):
                    pass
                except Exception as exc:
                    with owner.lock:
                        owner.errors.append(repr(exc))
                    self.close_connection = True
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    @property
    def url(self):
        return f"http://127.0.0.1:{self.server.server_port}/_bulk"

    def set_policy(self, policy):
        with self.lock:
            self.policy = policy

    def snapshot(self, attempts=False):
        with self.lock:
            return list(self.attempts if attempts else self.received)

    def keys(self, attempts=False):
        return {d["event"] for d in self.snapshot(attempts)}

    def close(self):
        self.release.set()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(5)


class App:
    def __init__(self, root, sinks, compressed=False, queue=1024, durable=True, file_limit=None):
        self.root, self.sinks = root, sinks
        self.wal = root / "wal"
        self.wal.mkdir(exist_ok=True)
        self.number = 0
        self.proc = None
        self.compressed, self.queue, self.durable = compressed, queue, durable
        self.file_limit = file_limit
        self.overrides = None
        self.source_lock = threading.Lock()
        self.source_docs = {}

    def start(self):
        self.number += 1
        # The app owns its listener; an unexpected bind/start failure fails the test.
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            self.port = sock.getsockname()[1]
        cfg = {"settings": {
            "journal": {"buf_dir_path": str(self.wal), "buf_file_bytes": 1048576,
                        "is_compress": self.compressed, "committed_id_sec": 1,
                        "journal_out_chan_len": self.queue, "child_data_chan_len": self.queue,
                        "child_id_chan_len": self.queue, "commit_id_chan_len": self.queue,
                        "gc_inteval_sec": 3600},
            "acceptor": {"async_out_chan_size": self.queue, "sync_out_chan_size": self.queue,
                         "recvs": {"plugins": {"http": {
                             "type": "http", "active_env": ["prod"], "path": "/ingest/:env",
                             "tag": "ingress", "orig_tag": "events", "tag_key": "tag",
                             "time_key": "ts", "time_format": "2006-01-02T15:04:05Z07:00",
                             "ts_regexp": "^.{20}$", "signature_key": "sig", "signature_salt": SALT,
                             "max_body_byte": 1048576, "max_allowed_delay_sec": 3600,
                             "max_allowed_ahead_sec": 60, "require_durable_ack": self.durable}}}},
            "acceptor_filters": {"fork": 2, "out_buf_len": self.queue},
            "tag_filters": {"internal_chan_size": self.queue},
            "dispatcher": {"nfork": 2, "out_chan_size": self.queue},
            "post_filters": {"fork": 2, "out_chan_size": self.queue,
                             "plugins": {"routing": {"type": "es-dispatcher", "tags": ["ingress"],
                                                      "tag_key": "tag", "rewrite_tag_map": {"events.prod": "storage.prod"}}}},
            "producer": {"forks": 2, "discard_chan_size": self.queue, "sender_inchan_size": self.queue,
                         "plugins": {f"sink{i}": {"type": "es", "active_env": ["prod"], "addr": sink.url,
                                                  "tags": ["storage.prod"], "indices": {"storage.prod": "delivery"},
                                                  "msg_batch_size": 7, "max_wait_sec": 1, "forks": 1,
                                                  "is_discard_when_blocked": False}
                                     for i, sink in enumerate(self.sinks)}}}}
        group_maximum = os.environ.get("DELIVERY_GROUP_MAX_MESSAGES")
        if group_maximum is not None:
            cfg["settings"]["journal"]["group_commit_max_messages"] = int(group_maximum)
        if self.overrides:
            self.overrides(cfg["settings"])
        path = self.root / "config.json"
        path.write_text(json.dumps(cfg))
        (self.root / f"config-{self.number}.json").write_text(json.dumps(cfg))
        self.logpath = self.root / f"process-{self.number}.log"
        self.logfile = self.logpath.open("wb")
        argv = [os.environ["GO_FLUENTD_BINARY"], "--config", str(path),
                "--env", "prod", "--addr", f"127.0.0.1:{self.port}", "--log-level", "error"]
        if self.file_limit is not None:
            # A separate single-threaded launcher avoids preexec_fn in our threaded
            # fixture. This is an actual OS write limit, not a Go test hook.
            import sys
            argv = [sys.executable, "-c",
                    "import os,resource,sys; n=int(sys.argv[1]); "
                    "resource.setrlimit(resource.RLIMIT_FSIZE,(n,n)); os.execv(sys.argv[2],sys.argv[2:])",
                    str(self.file_limit), *argv]
        self.proc = subprocess.Popen(argv, stdout=self.logfile, stderr=subprocess.STDOUT)

        def ready():
            if self.proc.poll() is not None:
                raise AssertionError("application exited during startup:\n" + self.logpath.read_text(errors="replace")[-8000:])
            try:
                return request(self.port, "GET", "/health", timeout=.2)[0] == 200
            except OSError:
                return False
        wait_for(ready, "application did not start")

    def crash(self):
        if self.proc is None:
            return
        proc, self.proc = self.proc, None
        status = proc.poll()
        if status is None:
            proc.kill()
            proc.wait(5)
        self.logfile.close()
        if status is not None:
            raise AssertionError(f"unexpected application exit {status}:\n" +
                                 self.logpath.read_text(errors="replace")[-8000:])

    def send(self, event, invalid=False):
        timestamp = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
        document = dict(event, ts=timestamp, sig=("invalid" if invalid else hashlib.md5((timestamp + SALT).encode()).hexdigest()))
        with self.source_lock:
            self.source_docs[event["event"]] = document
            with (self.root / "wire-requests.jsonl").open("a", encoding="utf8") as output:
                output.write(json.dumps(document, ensure_ascii=False, sort_keys=True) + "\n")
                output.flush()
                os.fsync(output.fileno())
        return request(self.port, "POST", "/ingest/prod", json.dumps(document, ensure_ascii=False).encode())


def events(prefix, count, seed=17):
    rng = random.Random(seed ^ SEED)
    return [{"event": f"{prefix}-{i:04d}", "payload": f"{rng.getrandbits(128):032x} λ 日志\n{i}",
             "seq": i, "tenant": f"tenant-{i % 5}", "nested": {"origin": prefix}}
            for i in range(count)]


def verify_manifest(expected, received):
    """Independent oracle. Repeated identical delivery is allowed, corruption is not."""
    manifest = {e["event"]: e for e in expected}
    missing = set(manifest)
    identities = {}
    previous_payloads = {}
    for actual in received:
        key = actual.get("event")
        if key not in manifest:
            raise AssertionError(f"unexpected/fabricated event {key!r}")
        e = manifest[key]
        want = {k: v for k, v in e.items() if k != "nested"}
        want["nested__origin"] = e["nested"]["origin"]
        allowed = set(want) | {"msgid", "tag", "ts", "sig", "transport_tag"}
        unexpected = set(actual) - allowed
        if unexpected:
            raise AssertionError(f"unexpected fields for {key}: {sorted(unexpected)}")
        for field, value in want.items():
            if actual.get(field) != value:
                raise AssertionError(f"payload corruption: {key} {field}: {actual.get(field)!r} != {value!r}")
        identity = actual.get("msgid")
        if not isinstance(identity, str) or not identity:
            raise AssertionError(f"missing delivery identity for {key}")
        previous = identities.setdefault(identity, key)
        if previous != key:
            raise AssertionError(f"delivery identity reused for different events: {identity}: {previous}, {key}")
        if actual.get("tag") != "events.prod":
            raise AssertionError(f"source routing metadata corrupted for {key}")
        previous_payload = previous_payloads.setdefault(key, actual)
        if previous_payload != actual:
            raise AssertionError(f"duplicate changed its payload or delivery identity: {key}")
        missing.discard(key)
    if missing:
        raise AssertionError(f"missing {len(missing)}/{len(manifest)} events: {sorted(missing)[:12]}")


class DeliveryTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="delivery-")
        self.root = Path(self.temp.name)
        self.sinks = []
        self.app = None

    def tearDown(self):
        problems = []
        if self.app:
            try:
                self.app.crash()
            except AssertionError as exc:
                problems.append(str(exc))
        for logpath in self.root.glob("process-*.log"):
            log = logpath.read_text(errors="replace")
            if any(marker in log for marker in ("WARNING: DATA RACE", "fatal error:", "panic:")):
                problems.append(f"process diagnostics in {logpath.name}:\n{log[-8000:]}")
        for sink in self.sinks:
            sink.close()
        artifact = os.environ.get("DELIVERY_ARTIFACTS")
        if artifact:
            import shutil
            target = Path(artifact) / self.id().split(".")[-1]
            if target.exists():
                shutil.rmtree(target)
            # Retain producer/sink ledgers and process output, not large WAL files.
            target.mkdir(parents=True)
            summary = {"sinks": [{"accepted": len(sink.snapshot()),
                                  "unique": len(sink.keys()),
                                  "duplicates": len(sink.snapshot()) - len(sink.keys()),
                                  "attempted": len(sink.snapshot(True)),
                                  "errors": sink.errors} for sink in self.sinks]}
            (self.root / "observations.json").write_text(json.dumps(summary, indent=2))
            for source in self.root.iterdir():
                if source.is_file():
                    shutil.copyfile(source, target / source.name)
        self.temp.cleanup()
        if problems:
            self.fail("\n".join(problems))

    def launch(self, sinks=1, **kwargs):
        self.sinks = [Ledger(self.root, f"sink-{i}") for i in range(sinks)]
        self.app = App(self.root, self.sinks, **kwargs)
        self.app.start()
        return self.app

    def send_all(self, expected, concurrency=1):
        with (self.root / "producer.jsonl").open("a", encoding="utf8") as ledger:
            for event in expected:
                ledger.write(json.dumps(event, ensure_ascii=False) + "\n")
            ledger.flush()
            os.fsync(ledger.fileno())
        with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as pool:
            replies = list(pool.map(self.app.send, expected))
        with (self.root / "responses.jsonl").open("a") as responses:
            for event, (code, body) in zip(expected, replies):
                responses.write(json.dumps({"event": event["event"], "status": code,
                                            "body": body.decode(errors="replace")}) + "\n")
                self.assertEqual(code, 200, (code, body))
                self.assertIsInstance(json.loads(body)["msgid"], int)

    def delivered(self, expected, sink=None):
        for target in ([sink] if sink else self.sinks):
            want = {e["event"] for e in expected}
            wait_for(lambda: want <= target.keys(),
                     lambda: f"missing sink deliveries {sorted(want - target.keys())[:16]}; "
                             f"sink errors={target.errors}; process={self.app.proc.poll()}\n" +
                             self.app.logpath.read_text(errors="replace")[-3500:])
            self.assertFalse(target.errors)
            with self.app.source_lock:
                full_expected = [dict(self.app.source_docs.get(e["event"], {}), **e) for e in expected]
            verify_manifest(full_expected, target.snapshot())

    def test_healthy_fanout_exact_content(self):
        self.launch(sinks=2)
        expected = events("healthy", 67)
        self.send_all(expected, concurrency=8)
        self.delivered(expected)
        for sink in self.sinks:
            self.assertEqual(len(sink.snapshot()), len(expected), "failure-free run duplicated messages")

    def test_crash_replay_plain(self):
        self.crash_replay(False)

    def test_crash_replay_gzip(self):
        self.crash_replay(True)

    def crash_replay(self, compressed):
        self.launch(compressed=compressed)
        self.sinks[0].set_policy("reject")
        expected = events("retained", 31)
        self.send_all(expected)
        wait_for(lambda: bool(self.sinks[0].snapshot(attempts=True)), "sink failure was never exercised")
        self.app.crash()
        self.sinks[0].set_policy("accept")
        self.app.start()
        self.delivered(expected)

    def test_successful_sink_cannot_acknowledge_failed_sink(self):
        self.launch(sinks=2)
        self.sinks[1].set_policy("reject")
        expected = events("fanout", 25)
        self.send_all(expected)
        self.delivered(expected, self.sinks[0])
        self.assertFalse(self.sinks[1].snapshot())
        self.app.crash()
        self.sinks[1].set_policy("accept")
        self.app.start()
        self.delivered(expected)

    def test_lost_ack_retries_do_not_corrupt(self):
        self.launch()
        self.sinks[0].set_policy("lose-ack")
        expected = events("ambiguous", 19)
        self.send_all(expected)
        self.delivered(expected)
        self.app.crash()
        before = len(self.sinks[0].snapshot())
        self.sinks[0].set_policy("accept")
        self.app.start()
        wait_for(lambda: len(self.sinks[0].snapshot()) >= before + len(expected), "lost acknowledgements were not retried after restart")
        verify_manifest(expected, self.sinks[0].snapshot())

    def test_restart_does_not_reuse_unacknowledged_ids(self):
        self.launch()
        sink = self.sinks[0]
        sink.set_policy("reject")
        old = events("old", 31)
        self.send_all(old)
        wait_for(lambda: len(sink.keys(attempts=True)) == len(old), "old messages never reached failing sink")
        self.app.crash()
        sink.set_policy("new-only")
        self.app.start()
        new = events("new", 31, 99)
        self.send_all(new)
        self.delivered(new)
        # Use captured independent attempts to catch ID aliasing immediately,
        # rather than relying on a particular scheduling of replay/commit.
        verify_manifest(old + new, sink.snapshot(attempts=True))
        self.app.crash()
        sink.set_policy("accept")
        self.app.start()
        self.delivered(old + new)

    def test_storage_refusal_cannot_return_success(self):
        self.launch()
        # Create an ordinary file exactly where a new journal directory belongs.
        # This is a real filesystem refusal, not a mocked write function.
        (self.app.wal / "ingress.prod").write_bytes(b"not a directory")
        try:
            status, body = self.app.send(events("storage-error", 1)[0])
        except (OSError, http.client.HTTPException) as exc:
            self.fail(f"storage failure must be handled, not crash the service: {exc}")
        self.assertGreaterEqual(status, 400, f"storage rejected record but producer received success: {status} {body!r}")
        self.assertFalse(self.sinks[0].snapshot())
        self.assertEqual(request(self.app.port, "GET", "/health")[0], 200)
        (self.app.wal / "ingress.prod").unlink()
        expected = events("storage-restored", 1)
        self.send_all(expected)
        self.delivered(expected)

    def test_invalid_signature_is_not_delivered(self):
        self.launch()
        code, _ = self.app.send(events("invalid", 1)[0], invalid=True)
        self.assertEqual(code, 400)
        expected = events("valid", 1)
        self.send_all(expected)
        self.delivered(expected)
        self.assertEqual(self.sinks[0].keys(), {"valid-0000"})

    def test_immediate_crash_after_durable_acceptance(self):
        # No sink observation or graceful shutdown is allowed before the kill.
        self.launch(compressed=True)
        self.sinks[0].set_policy("reject")
        expected = events("immediate", 1)
        self.send_all(expected)
        self.app.crash()
        self.sinks[0].set_policy("accept")
        self.app.start()
        self.delivered(expected)

    def test_tiny_queues_do_not_bypass_durable_acceptance(self):
        self.launch(compressed=True, queue=1)
        self.sinks[0].set_policy("reject")
        expected = events("pressure", 160)
        self.send_all(expected, concurrency=16)
        self.app.crash()
        # Remove downstream pressure so the liveness premise is actually true.
        self.app.queue = 1024
        self.sinks[0].set_policy("accept")
        self.app.start()
        self.delivered(expected)

    def test_repeated_crashes_preserve_all_required_sinks(self):
        self.launch(sinks=2, compressed=True)
        expected = []
        rng = random.Random(81891 ^ SEED)
        for phase in range(4):
            for sink in self.sinks:
                sink.set_policy(rng.choice(["reject", "accept"]))
            current = events(f"phase{phase}", 13, phase)
            expected.extend(current)
            self.send_all(current, concurrency=4)
            self.app.crash()
            self.app.start()
        self.app.crash()
        for sink in self.sinks:
            sink.set_policy("accept")
        self.app.start()
        self.delivered(expected)

    def test_acknowledgements_survive_ttl_expiry_and_restart(self):
        self.launch(compressed=True)
        expected = events("settled", 17)
        self.send_all(expected)
        self.delivered(expected)
        # The configured ID cache expires after 1s; the legacy periodic flush
        # defaults to 5s. This settling interval is not used as proof of delivery.
        time.sleep(6)
        self.app.crash()
        before = len(self.sinks[0].snapshot())
        self.app.start()
        sentinel = events("sentinel", 1)
        self.send_all(sentinel)
        self.delivered(expected + sentinel)
        time.sleep(3.5)  # Includes another replay pass, beyond the cache TTL.
        self.assertEqual(len(self.sinks[0].snapshot()), before + 1,
                         "disk-confirmed records were replayed after cache expiry")

    def test_write_failure_is_not_accepted_or_forwarded(self):
        self.launch(file_limit=8192)
        expected = events("write-refused", 1)[0]
        expected["payload"] = "z" * 20000
        status, body = self.app.send(expected)
        self.assertEqual(status, 503, (status, body))
        self.assertFalse(self.sinks[0].snapshot())
        self.assertEqual(request(self.app.port, "GET", "/health")[0], 200)

    def test_prefilter_rejection_is_not_durable_acceptance(self):
        self.launch()
        self.app.crash()
        self.app.overrides = lambda cfg: cfg["acceptor_filters"].update({"plugins": {"default": {
            "remove_unknown_tag": True, "accept_tags": ["other.prod"]}}})
        self.app.start()
        status, body = self.app.send(events("filtered", 1)[0])
        self.assertEqual(status, 503, (status, body))
        self.assertFalse(self.sinks[0].snapshot())

    def test_pending_multiline_survives_crash_without_early_tail_ack(self):
        self.launch(compressed=True)
        self.app.crash()
        def concat(cfg):
            cfg["dispatcher"]["nfork"] = 1
            cfg["tag_filters"]["plugins"] = {"concator": {"type": "concator", "config": {
                "nfork": 1, "lb_key": "tenant", "max_length": 100000}, "plugins": {
                "ingress": {"msg_key": "log", "identifier": "tenant", "regex": "^HEAD"}}}}
        self.app.overrides = concat
        self.app.start()
        source = events("multiline", 2)
        source[0]["log"] = "HEAD line-key-0\n"
        source[1]["log"] = " continuation line-key-1\n"
        source[1]["tenant"] = source[0]["tenant"]
        self.send_all(source)
        self.app.crash()
        self.app.start()
        expected = [dict(source[0], log=source[0]["log"] + source[1]["log"])]
        self.delivered(expected)

    def test_stalled_sink_is_not_satisfied_by_another_sink(self):
        self.launch(sinks=2, compressed=True)
        self.sinks[1].set_policy("hold")
        expected = events("stalled", 9)
        self.send_all(expected)
        self.delivered(expected, self.sinks[0])
        wait_for(lambda: bool(self.sinks[1].snapshot(True)), "stalled sink was never called")
        self.assertFalse(self.sinks[1].snapshot())
        self.app.crash()
        self.sinks[1].set_policy("accept")
        self.sinks[1].release.set()
        self.app.start()
        self.delivered(expected)

    def test_malformed_success_response_does_not_lose_records(self):
        self.launch(compressed=True)
        self.sinks[0].set_policy("malformed")
        expected = events("bad-response", 15)
        self.send_all(expected)
        wait_for(lambda: bool(self.sinks[0].snapshot(True)), "malformed response was not exercised")
        self.app.crash()
        self.assertFalse(self.sinks[0].snapshot())
        self.sinks[0].set_policy("accept")
        self.app.start()
        self.delivered(expected)

    def test_fluent_tcp_fragmented_frames_reach_all_sinks(self):
        self.launch(sinks=2)
        self.app.crash()
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            tcp_port = sock.getsockname()[1]
        def tcp_config(cfg):
            cfg["acceptor"]["recvs"]["plugins"].update({"fluent": {
                "type": "fluentd", "active_env": ["prod"], "addr": f"127.0.0.1:{tcp_port}",
                "tag_key": "transport_tag", "nfork": 1}})
            cfg["tag_filters"]["plugins"] = {"parse": {"type": "parser", "tags": ["ingress.prod"], "nfork": 1}}
        self.app.overrides = tcp_config
        self.app.start()
        def connectable():
            try:
                with socket.create_connection(("127.0.0.1", tcp_port), .2):
                    return True
            except OSError:
                return False
        wait_for(connectable, "Fluent TCP listener did not start")
        expected = events("tcp", 33)
        (self.root / "producer.jsonl").write_text("".join(json.dumps(e) + "\n" for e in expected))
        docs = [dict(e, tag="events.prod") for e in expected]
        timestamp = int(time.time())
        # Three protocol shapes, generated independently of the project's encoder.
        wire = pack(["ingress.prod", timestamp, docs[0]])
        wire += pack(["ingress.prod", [[timestamp, d] for d in docs[1:17]]])
        wire += pack(["ingress.prod", b"".join(pack([timestamp, d]) for d in docs[17:])])
        with socket.create_connection(("127.0.0.1", tcp_port), 5) as conn:
            for offset in range(0, len(wire), 37):
                conn.sendall(wire[offset:offset + 37])
        self.delivered(expected)
        for sink in self.sinks:
            self.assertEqual(len(sink.snapshot()), len(expected))

    def test_failed_append_preserves_previously_accepted_records(self):
        self.failed_append(False)

    def test_failed_gzip_append_preserves_previously_accepted_records(self):
        self.failed_append(True)

    def failed_append(self, compressed):
        self.launch(file_limit=8192, compressed=compressed)
        self.sinks[0].set_policy("reject")
        accepted = events("accepted-prefix", 1)
        self.send_all(accepted)
        rejected = events("torn-tail", 1)[0]
        rejected["payload"] = random.Random(77).randbytes(20000).hex()
        status, body = self.app.send(rejected)
        self.assertEqual(status, 503, (status, body))
        self.app.crash()
        self.app.file_limit = None
        self.sinks[0].set_policy("accept")
        self.app.start()
        self.delivered(accepted)


    def concurrent_acceptance_cut(self, compressed):
        self.launch(sinks=2, compressed=compressed, queue=512)
        for sink in self.sinks:
            sink.set_policy("reject")
        expected = events("concurrent-cut", 257)
        self.send_all(expected, concurrency=32)
        # The only barrier before SIGKILL is the external HTTP responses.
        # No sink wait, extra rotation, journal helper or graceful close.
        self.app.crash()
        for sink in self.sinks:
            sink.set_policy("accept")
        self.app.start()
        self.delivered(expected)
        for sink in self.sinks:
            verify_manifest(expected, [json.loads(line) for line in sink.path.read_text().splitlines()])

    def test_concurrent_accepted_group_survives_kill_plain(self):
        self.concurrent_acceptance_cut(False)

    def test_concurrent_accepted_group_survives_kill_gzip(self):
        self.concurrent_acceptance_cut(True)

    def unfinished_request_cut(self, compressed):
        self.launch(sinks=2, compressed=compressed, queue=512)
        for sink in self.sinks:
            sink.set_policy("reject")
        expected = events("unfinished", 257)
        with (self.root / "producer.jsonl").open("w") as output:
            for item in expected:
                output.write(json.dumps(item) + "\n")
            output.flush()
            os.fsync(output.fileno())
        def submit(item):
            try:
                code, body = self.app.send(item)
                return {"event": item["event"], "status": code,
                        "body": body.decode(errors="replace")}
            except (OSError, http.client.HTTPException) as exc:
                return {"event": item["event"], "status": None, "network_error": repr(exc)}
        results = []
        with concurrent.futures.ThreadPoolExecutor(max_workers=32) as pool:
            futures = [pool.submit(submit, item) for item in expected]
            accepted_so_far = 0
            crashed = False
            for future in concurrent.futures.as_completed(futures):
                observation = future.result(timeout=20)
                results.append(observation)
                accepted_so_far += observation["status"] == 200
                if accepted_so_far >= 8 and not crashed:
                    self.app.crash()
                    crashed = True
        self.assertTrue(crashed, "did not exercise the requested crash cut")
        (self.root / "responses.jsonl").write_text("".join(json.dumps(r) + "\n" for r in results))
        accepted = {r["event"] for r in results if r["status"] == 200}
        self.assertGreaterEqual(len(accepted), 8)
        self.assertLess(len(accepted), len(expected), "fixture never exercised unfinished requests")
        for r in results:
            self.assertIn(r["status"], (None, 200, 503))
        for sink in self.sinks:
            sink.set_policy("accept")
        self.app.start()
        sentinel = events("new-after-cut", 1)[0]
        self.send_all([sentinel])
        accepted.add(sentinel["event"])
        submitted = {item["event"]: item for item in expected + [sentinel]}
        for sink in self.sinks:
            wait_for(lambda: accepted <= sink.keys(), "an acknowledged request was lost after a mixed crash cut")
            received = [json.loads(line) for line in sink.path.read_text().splitlines()]
            keys = {row["event"] for row in received}
            self.assertTrue(keys <= submitted.keys(), "fabricated record after crash")
            # Unanswered requests may exist or be absent. Every observed record
            # must still match an actual submission, including duplicate IDs.
            verify_manifest([submitted[key] for key in keys], received)
            self.assertFalse(sink.errors)

    def test_partial_response_group_cut_plain(self):
        self.unfinished_request_cut(False)

    def test_partial_response_group_cut_gzip(self):
        self.unfinished_request_cut(True)

    def concurrent_append_refusal(self, compressed):
        self.launch(sinks=2, compressed=compressed, file_limit=8192)
        for sink in self.sinks:
            sink.set_policy("reject")
        accepted = events("before-refusal", 1)
        self.send_all(accepted)
        refused = events("group-refused", 32)
        rng = random.Random(SEED ^ 99127)
        # High-entropy input also exceeds the kernel's limit when compressed.
        for item in refused:
            item["payload"] = rng.randbytes(32768).hex()
        with concurrent.futures.ThreadPoolExecutor(max_workers=16) as pool:
            results = list(pool.map(self.app.send, refused))
        (self.root / "refused-responses.jsonl").write_text("".join(json.dumps({"event": e["event"], "status": r[0]}) + "\n" for e, r in zip(refused, results)))
        self.assertTrue(all(code == 503 for code, _ in results), "kernel-refused append reported acceptance")
        self.assertIn("file too large", self.app.logpath.read_text(errors="replace").lower())
        for sink in self.sinks:
            self.assertFalse({item["event"] for item in refused} & sink.keys(True), "failed live group was forwarded")
        self.app.crash()
        self.app.file_limit = None
        for sink in self.sinks:
            sink.set_policy("accept")
        self.app.start()
        self.delivered(accepted)

    def test_concurrent_kernel_refusal_preserves_accepted_plain(self):
        self.concurrent_append_refusal(False)

    def test_concurrent_kernel_refusal_preserves_accepted_gzip(self):
        self.concurrent_append_refusal(True)

    def test_oracle_rejects_loss_corruption_and_fabrication(self):
        expected = events("oracle", 2)
        good = [dict({k: v for k, v in e.items() if k != "nested"}, **{"nested__origin": e["nested"]["origin"], "msgid": str(i), "tag": "events.prod"}) for i, e in enumerate(expected)]
        verify_manifest(expected, good + good)
        for bad in [good[:-1], [dict(good[0], payload="bad"), good[1]],
                    good + [dict(good[0], event="fabricated")],
                    [good[0], dict(good[1], msgid=good[0]["msgid"])]]:
            with self.assertRaises(AssertionError):
                verify_manifest(expected, bad)


if __name__ == "__main__":
    unittest.main()
