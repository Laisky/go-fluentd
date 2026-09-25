#!/usr/bin/env python3
"""Real executable HTTP events -> disk journal -> independent fsynced HTTP sinks.

The peer implements wire parsing independently with Python's standard library.
Only the existing test suite's process launcher/readiness helpers are reused.
"""
from __future__ import annotations

import argparse
import base64
import copy
import hashlib
import http.client
import json
import os
from pathlib import Path
import random
import socket
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import quote, unquote

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "delivery"))
from test_delivery import App, wait_for  # process control only, not a wire oracle

TOKEN = "event-test-only"


def canonical(value):
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def fixtures(mode, count, seed):
    rng = random.Random(seed)
    result = []
    for i in range(count):
        data = {"event": f"event-{i}", "text": f"世界 / café / {rng.getrandbits(64):016x}\n",
                "big": 18446744073709551615, "small": -9223372036854775808,
                "nested": {"on": True, "nil": None, "list": [1, "two", False]}}
        if mode == "ndjson":
            data.update({"msgid": f"caller-{i}", "a.b": "must not rename", "": "must not drop"})
            result.append(data)
        else:
            result.append({"specversion": "1.0", "id": f"event-{i}", "source": "/original/source",
                           "type": "example.audit", "subject": "世界 / 100%", "msgid": f"caller-{i}",
                           "datacontenttype": "application/json", "data": data})
    return result


def wire(mode, records):
    headers = {"Authorization": "Bearer " + TOKEN}
    if mode == "ndjson":
        headers["Content-Type"] = "application/x-ndjson"
        body = "".join(canonical(r) + "\n" for r in records).encode()
    elif mode == "batch":
        headers["Content-Type"] = "application/cloudevents-batch+json"
        body = canonical(records).encode()
    elif mode == "structured":
        assert len(records) == 1
        headers["Content-Type"] = "application/cloudevents+json"
        body = canonical(records[0]).encode()
    else:
        assert mode == "binary" and len(records) == 1
        event = records[0]
        headers.update({"ce-" + k: quote(v, safe=" !#$&'()*+,-./:;<=>?@[]^_`{|}~")
                        for k, v in event.items() if k not in ("data", "datacontenttype")})
        headers["Content-Type"] = event["datacontenttype"]
        body = canonical(event["data"]).encode()
    return body, headers


def parse_wire(mode, headers, body):
    media = headers.get("content-type", "").split(";", 1)[0]
    if mode == "ndjson":
        assert media == "application/x-ndjson" and body.endswith(b"\n"), "NDJSON framing"
        return [json.loads(line) for line in body.splitlines() if line]
    if mode == "batch":
        assert media == "application/cloudevents-batch+json", "CloudEvents batch media"
        records = json.loads(body)
        assert isinstance(records, list)
        return records
    if mode == "structured":
        assert media == "application/cloudevents+json", "structured media"
        return [json.loads(body)]
    assert mode == "binary" and media == "application/json", "binary media"
    event = {k[3:]: unquote(v) for k, v in headers.items() if k.startswith("ce-")}
    event["datacontenttype"] = headers["content-type"]
    event["data"] = json.loads(body)
    return [event]


def post(port, body, headers, chunked=False):
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=8)
    try:
        payload = [body[:3], body[3:]] if chunked else body
        conn.request("POST", "/events", payload, headers, encode_chunked=chunked)
        response = conn.getresponse()
        return response.status, response.read()
    finally:
        conn.close()


class Sink:
    def __init__(self, root, name, mode):
        self.path, self.mode = root / (name + ".jsonl"), mode
        self.lock = threading.Lock()
        self.policy, self.errors, self.attempts = "accept", [], 0
        owner = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *_):
                pass

            def handle(self):
                try:
                    super().handle()
                except (BrokenPipeError, ConnectionResetError):
                    pass  # expected when the application is killed mid-connection

            def do_POST(self):
                try:
                    assert self.path == "/receive", "wrong destination path"
                    assert self.headers.get("Authorization") == "Bearer " + TOKEN, "destination auth"
                    size = int(self.headers["Content-Length"])
                    assert 0 < size <= 4 << 20, "output size bound"
                    body = self.rfile.read(size)
                    headers = {k.lower(): v for k, v in self.headers.items()}
                    records = parse_wire(mode, headers, body)
                    assert all(isinstance(r, dict) for r in records)
                    with owner.lock:
                        policy = owner.policy
                        owner.attempts += 1
                        if policy != "reject":
                            with owner.path.open("a", encoding="utf8") as stream:
                                stream.write(canonical({"body": base64.b64encode(body).decode(),
                                                        "headers": {k: v for k, v in headers.items() if k != "authorization"},
                                                        "records": records}) + "\n")
                                stream.flush()
                                os.fsync(stream.fileno())
                    if policy == "lose-ack":
                        self.connection.shutdown(socket.SHUT_RDWR)
                        self.close_connection = True
                        return
                    self.send_response(503 if policy == "reject" else 204)
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                except (BrokenPipeError, ConnectionResetError):
                    pass
                except Exception as exc:
                    with owner.lock:
                        owner.errors.append(repr(exc))
                    self.close_connection = True

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=lambda: self.server.serve_forever(poll_interval=.02), daemon=True)
        self.thread.start()

    @property
    def url(self):
        return f"http://127.0.0.1:{self.server.server_port}/receive"

    def set_policy(self, value):
        with self.lock:
            self.policy = value

    def records(self):
        with self.lock:
            if not self.path.exists():
                return []
            return [r for line in self.path.read_text().splitlines() for r in json.loads(line)["records"]]

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(2)


def key(record):
    return record.get("id", record.get("event"))


def audit(root, mode, seed, count):
    # Regenerate the caller's original fixture, not just compare two mutable files.
    expected = fixtures(mode, count, seed)
    assert json.loads((root / "manifest.json").read_text()) == expected
    receipts = json.loads((root / "receipts.json").read_text())
    assert len(receipts) == count and all(r == 204 for r in receipts)
    want = {key(r): canonical(r) for r in expected}
    delivered, duplicates = 0, 0
    for name in ("sink0", "sink1"):
        seen, ids, event_ids = {}, {}, {}
        for line in (root / (name + ".jsonl")).read_text().splitlines():
            row = json.loads(line)
            parsed = parse_wire(mode, row["headers"], base64.b64decode(row["body"], validate=True))
            assert canonical(parsed) == canonical(row["records"]), "wire/ledger discrepancy"
            for record in parsed:
                identity = key(record)
                assert identity in want and canonical(record) == want[identity], "unexpected/changed event"
                transport = row["headers"].get("x-go-fluentd-id")
                if mode in ("structured", "binary"):
                    assert transport, "missing transport ID"
                if transport:
                    assert len(parsed) == 1
                    assert ids.setdefault(transport, identity) == identity, "WAL ID collision"
                    old = event_ids.setdefault(identity, transport)
                    assert old == transport, "retry changed transport identity"
                if identity in seen:
                    duplicates += 1
                seen[identity] = transport or "batch"
            delivered += len(parsed)
        assert set(seen) == set(want), f"missing events at {name}: {set(want)-set(seen)}"
    return {"accepted": count, "required_deliveries": count * 2, "rows": delivered, "identical_retries": duplicates}


def scenario(root, mode, compressed, group, seed, storage_refused=False):
    root.mkdir(parents=True)
    count, pending = 13, 9
    expected = fixtures(mode, count, seed)
    (root / "manifest.json").write_text(canonical(expected))
    sinks = [Sink(root, f"sink{i}", mode) for i in range(2)]
    app = App(root, sinks, compressed=compressed, queue=64)
    receipts = []

    def config(settings):
        fmt = "ndjson" if mode == "ndjson" else "cloudevents"
        settings["journal"]["group_commit_max_messages"] = group
        settings["acceptor"]["recvs"]["plugins"] = {"events": {
            "type": "http_events", "active_env": ["prod"], "path": "/events", "tag": "source.{env}",
            "format": fmt, "bearer_token": TOKEN, "max_body_byte": 8192, "max_records": 16, "ack_timeout_sec": 3}}
        settings["post_filters"]["plugins"] = {}
        settings["producer"]["plugins"] = {f"events{i}": {
            "type": "http_events", "active_env": ["prod"], "addr": sink.url, "tags": ["source.{env}"],
            "format": fmt, "mode": "" if mode == "ndjson" else mode, "bearer_token": TOKEN,
            "msg_batch_size": 4 if mode in ("ndjson", "batch") else 1,
            "max_wait_msec": 25, "max_attempts": 2, "retry_backoff_msec": 10,
            "request_timeout_sec": 2, "max_retry_delay_sec": 1, "forks": 1}
            for i, sink in enumerate(sinks)}

    app.overrides = config
    if storage_refused:
        (app.wal / "source.prod").write_text("filesystem refuses a tag directory")
    try:
        sinks[1].set_policy("reject")
        app.start()
        # Reject a whole malformed batch: its valid prefix must never be published.
        sample_body, sample_headers = wire(mode, [expected[0]])
        invalid_headers = dict(sample_headers, Authorization="Bearer incorrect")
        assert post(app.port, sample_body, invalid_headers)[0] == 401
        assert post(app.port, b"x" * 8193, sample_headers, chunked=True)[0] == 413
        assert post(app.port, sample_body, dict(sample_headers, **{"Content-Encoding": "gzip"}))[0] == 415
        rejected = copy.deepcopy(expected[0])
        rejected["event" if mode == "ndjson" else "id"] = "never-accepted"
        if mode == "ndjson":
            invalid, invalid_headers = wire(mode, [rejected])
            invalid += b"[]\n"
        else:
            invalid = canonical([rejected, {"specversion": "1.0"}]).encode()
            invalid_headers = dict(sample_headers, **{"Content-Type": "application/cloudevents-batch+json"})
        assert post(app.port, invalid, invalid_headers)[0] == 400
        assert not any(s.attempts for s in sinks), "rejected request reached a sender"
        if storage_refused:
            status, _ = post(app.port, sample_body, sample_headers)
            assert status == 503, f"storage refusal returned {status}"
            time.sleep(.1)
            assert not any(s.attempts for s in sinks), "storage-rejected event forwarded"
            return {"accepted": 0, "required_deliveries": 0, "storage_status": status}
        # Send requests through actual ingress; HTTP 204 must not depend on sink1 succeeding.
        for first, last in ((0, pending),):
            step = 3 if mode in ("ndjson", "batch") else 1
            for i in range(first, last, step):
                group_records = expected[i:min(i + step, last)]
                input_mode = mode if mode == "ndjson" else ("batch" if len(group_records) > 1 else ("structured", "binary", "batch")[i % 3])
                body, headers = wire(input_mode, group_records)
                status, _ = post(app.port, body, headers, chunked=i % 2 == 0)
                assert status == 204, f"durable response {status}"
                receipts.extend([status] * len(group_records))
        wait_for(lambda: sinks[1].attempts > 0, "failing sink was not attempted")
        assert not sinks[1].records(), "rejecting sink accepted records"
        app.crash()  # SIGKILL, no graceful shutdown/forced rotation.
        sinks[1].set_policy("accept")
        sinks[0].set_policy("lose-ack")
        app.start()  # same on-disk WAL
        for i, event in enumerate(expected[pending:]):
            input_mode = mode if mode == "ndjson" else ("structured", "binary", "batch")[i % 3]
            body, headers = wire(input_mode, [event])
            status, _ = post(app.port, body, headers)
            assert status == 204
            receipts.append(status)
        for sink in sinks:
            wait_for(lambda s=sink: {key(r) for r in s.records()} >= {key(r) for r in expected},
                     lambda s=sink: f"incomplete deliveries; peer errors={s.errors}")
        app.crash()
        sinks[0].set_policy("accept")
        before = [len(s.records()) for s in sinks]
        app.start()
        # Require fresh deliveries in this process generation, not stale successes.
        for sink, offset in zip(sinks, before):
            wait_for(lambda s=sink, n=offset: {key(r) for r in s.records()[n:]} >= {key(r) for r in expected},
                     "lost-ACK records did not replay after the second crash")
        app.crash()
        assert not any(s.errors for s in sinks), [s.errors for s in sinks]
        (root / "receipts.json").write_text(canonical(receipts))
        return audit(root, mode, seed, count)
    finally:
        app.crash()
        for sink in sinks:
            sink.close()
        for path in root.glob("process-*.log"):
            text = path.read_text(errors="replace")
            assert not any(w in text for w in ("WARNING: DATA RACE", "panic:", "fatal error:")), text[-4000:]


def main():
    if not __debug__:
        raise RuntimeError("these assertion-based contracts must not run with Python -O")
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--binary", type=Path, required=True)
    p.add_argument("--artifacts", type=Path, required=True)
    p.add_argument("--seed", type=int, default=127)
    p.add_argument("--group", type=int, choices=(1, 64), default=1)
    p.add_argument("--mode", choices=("ndjson", "structured", "binary", "batch"))
    args = p.parse_args()
    binary = args.binary.resolve(strict=True)
    os.environ["GO_FLUENTD_BINARY"] = str(binary)
    args.artifacts.mkdir(parents=True, exist_ok=True)
    modes = [args.mode] if args.mode else ["ndjson", "structured", "binary", "batch"]
    cases = [(m, gz, False) for m in modes for gz in (False, True)]
    cases += [(m, False, True) for m in modes if m in ("ndjson", "structured")]
    random.Random(args.seed).shuffle(cases)
    results = []
    for mode, gz, refused in cases:
        name = f"{mode}-gzip{int(gz)}-group{args.group}" + ("-storage-refused" if refused else "")
        print(name, flush=True)
        start = time.monotonic()
        try:
            record = scenario(args.artifacts / name, mode, gz, args.group, args.seed, refused)
            record.update(case=name, passed=True)
        except Exception:
            import traceback
            record = dict(case=name, passed=False, error=traceback.format_exc())
            print(record["error"], flush=True)
        record["seconds"] = time.monotonic() - start
        results.append(record)
        report = {"binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(), "seed": args.seed,
                  "group": args.group, "expected": len(cases), "results": results}
        (args.artifacts / "results.json").write_text(json.dumps(report, indent=2))
    return 0 if len(results) == len(cases) and all(r["passed"] for r in results) else 1


if __name__ == "__main__":
    raise SystemExit(main())
