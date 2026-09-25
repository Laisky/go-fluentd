#!/usr/bin/env python3
"""Real Collector -> configured go-fluentd -> real Collector interoperability.

Only Python's standard library and independent test fixtures are used. Transparent
recording peers retain fsynced ingress/egress bytes and a terminal JSON ledger.
Collector queues/retries are disabled, so they cannot conceal application loss.
"""
from __future__ import annotations

import argparse
import base64
import gzip
import hashlib
import http.server
import json
from pathlib import Path
import signal
import socket
import subprocess
import sys
import threading
import urllib.error
import urllib.request

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'otlp_service'))
from run import Process, config, dump, payload, port, request, until

SIGNALS = ('logs', 'metrics', 'traces')
CONTENT_TYPES = ('application/json', 'application/x-protobuf')
TOKEN = 'collector-interoperability-fixture-only'


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def records(path: Path) -> list[dict]:
    return [json.loads(line) for line in path.read_text().splitlines()]


def decoded(row: dict) -> bytes:
    raw = base64.b64decode(row['body'], validate=True)
    assert row['encoding'] in ('', 'gzip'), 'unexpected compression'
    return gzip.decompress(raw) if row['encoding'] else raw


def one_item(obj: dict, sig: str) -> dict:
    names = {'logs': ('resourceLogs', 'scopeLogs', 'logRecords'),
             'metrics': ('resourceMetrics', 'scopeMetrics', 'metrics'),
             'traces': ('resourceSpans', 'scopeSpans', 'spans')}
    for key in names[sig]:
        values = obj[key]
        assert len(values) == 1, f'{key}: expected exactly one element'
        obj = values[0]
    return obj


def verify_sink(raw: bytes, sig: str, ct: str, seq: int) -> None:
    """Compare the actual Collector output with caller values, not output counts."""
    expected = one_item(json.loads(payload(sig, 'application/json', seq)), sig)
    # The existing independent protobuf fixture uses 0x21+seq; its JSON
    # counterpart uses 0x22/0x33. Compare with the actual caller's encoding.
    if sig == 'traces' and ct == 'application/x-protobuf':
        expected['spanId'] = bytes([0x21 + seq] * 8).hex()
    actual = one_item(json.loads(raw), sig)
    if sig == 'logs':
        assert actual['body'] == expected['body'], 'log body changed'
        assert actual['timeUnixNano'] == expected['timeUnixNano'], 'log timestamp changed'
    elif sig == 'metrics':
        assert actual['name'] == expected['name'], 'metric name changed'
        assert len(actual['gauge']['dataPoints']) == 1, 'metric points changed'
        point, want = actual['gauge']['dataPoints'][0], expected['gauge']['dataPoints'][0]
        assert point['asInt'] == want['asInt'], 'int64 metric value changed'
        assert point['timeUnixNano'] == want['timeUnixNano'], 'metric timestamp changed'
    else:
        for key in ('name', 'traceId', 'spanId', 'startTimeUnixNano', 'endTimeUnixNano'):
            assert actual[key] == expected[key], f'span {key} changed'


def audit(root: Path) -> dict:
    meta = json.loads((root / 'case.json').read_text())
    sig, ct = meta['signal'], meta['content_type']
    lifecycle = records(root / 'lifecycle.jsonl')
    assert [(r['phase'], r['event']) for r in lifecycle] == [
        (1, 'start'), (1, 'stop'), (2, 'start'), (2, 'stop'), (3, 'start'), (3, 'stop')
    ], 'missing process lifecycle receipt'
    starts, stops = lifecycle[::2], lifecycle[1::2]
    assert len({r['generation_sha256'] for r in starts}) == 1, 'generation changed across restart'
    assert [r['exit_code'] for r in stops] == [-signal.SIGKILL, -signal.SIGKILL, 0], 'crash or graceful exit not observed'
    assert all(a['pid'] == b['pid'] for a, b in zip(starts, stops)), 'process receipt mismatch'
    submitted = records(root / 'source.jsonl')
    assert len(submitted) == 3, 'missing caller receipts'
    assert [r['sequence'] for r in submitted] == [99, 1, 2], 'caller sequence changed'
    assert submitted[0]['status'] != 200, 'storage refusal acknowledged by source Collector'
    for seq, row in enumerate(submitted[1:], 1):
        assert row['status'] == 200, 'source Collector did not accept valid input'
        assert base64.b64decode(row['body'], validate=True) == payload(sig, ct, seq), 'caller workload changed'
    wire = records(root / 'wire.jsonl')
    incoming = [r for r in wire if r['hop'] == 'ingress']
    assert len(incoming) == 3 and sum(r['status'] == 503 for r in incoming) == 1, 'missing WAL refusal'
    admitted = [r for r in incoming if r['status'] == 200]
    assert len(admitted) == 2, 'unexpected local acceptance count'
    expected = {digest(decoded(r)): decoded(r) for r in admitted}
    assert len(expected) == 2, 'distinct inputs collapsed'
    outgoing = [r for r in wire if r['hop'] == 'egress']
    assert any(r['status'] == 503 for r in outgoing), 'retry path not exercised'
    counts = dict.fromkeys(expected, 0)
    for row in incoming + outgoing:
        assert row['path'] == '/v1/' + sig, 'signal route changed'
        assert row['content_type'] == ct, 'content type changed'
        assert row['encoding'] == 'gzip', 'gzip transport not exercised'
    for row in outgoing:
        raw = decoded(row)
        assert digest(raw) in expected and raw == expected[digest(raw)], 'application changed or invented wire payload'
        assert row['status'] in (200, 503), 'unexpected egress status'
        if row['status'] == 200:
            counts[digest(raw)] += 1
    assert list(counts.values()) == [1, 1], 'lost or repeated known-persisted delivery'
    terminal = [r for r in wire if r['hop'] == 'sink']
    assert len(terminal) == 2, 'missing or duplicate terminal records'
    for seq, row in enumerate(terminal, 1):
        assert row['path'] == '/v1/' + sig and row['content_type'] == 'application/json', 'sink metadata changed'
        assert row['status'] == 200, 'sink rejected record'
        verify_sink(decoded(row), sig, ct, seq)
    return {'accepted_envelopes': 2, 'terminal_items': 2, 'wal_refusals': 1,
            'retry_attempts': sum(r['status'] == 503 for r in outgoing), 'sigkills': 2}


def read_body(handler: http.server.BaseHTTPRequestHandler) -> bytes:
    """Support streaming chunked requests as well as Content-Length."""
    limit = 1 << 20
    transfer = handler.headers.get('Transfer-Encoding', '').lower()
    if transfer:
        assert transfer == 'chunked', 'unsupported transfer encoding'
        body = bytearray()
        while True:
            line = handler.rfile.readline(128)
            size = int(line.split(b';', 1)[0].strip(), 16)
            if not size:
                assert handler.rfile.readline(128) == b'\r\n', 'unexpected trailers'
                return bytes(body)
            assert size > 0 and len(body) + size <= limit, 'chunk limit exceeded'
            chunk = handler.rfile.read(size)
            assert len(chunk) == size and handler.rfile.read(2) == b'\r\n', 'incomplete chunk'
            body.extend(chunk)
    size = int(handler.headers.get('Content-Length', '0'))
    assert 0 <= size <= limit, 'body limit exceeded'
    body = handler.rfile.read(size)
    assert len(body) == size, 'incomplete request body'
    return body


class Collector:
    def __init__(self, binary: Path, root: Path, name: str, listen: int,
                 endpoint: str, encoding: str, compression: str, token: str = ''):
        exporter = {'endpoint': endpoint, 'encoding': encoding, 'compression': compression,
                    'timeout': '3s', 'retry_on_failure': {'enabled': False},
                    'sending_queue': {'enabled': False}}
        if token:
            exporter['headers'] = {'Authorization': 'Bearer ' + token}
        cfg = {'receivers': {'otlp': {'protocols': {'http': {'endpoint': f'127.0.0.1:{listen}'}}}},
               'exporters': {'otlp_http': exporter}, 'service': {
                   'telemetry': {'metrics': {'level': 'none'}, 'logs': {'level': 'error'}},
                   'pipelines': {s: {'receivers': ['otlp'], 'exporters': ['otlp_http']} for s in SIGNALS}}}
        path = root / (name + '.json')
        path.write_text(json.dumps(cfg, indent=2))
        self.log = open(root / (name + '.log'), 'wb')
        self.p = subprocess.Popen([str(binary), '--config=file:' + str(path)], stdout=self.log, stderr=subprocess.STDOUT)
        try:
            def ready():
                assert self.p.poll() is None, f'{name} exited; see {name}.log'
                with socket.create_connection(('127.0.0.1', listen), timeout=.2):
                    return True
            until(ready, name + ' did not start')
        except BaseException:
            self.stop(kill=True)
            raise

    def stop(self, kill: bool = False) -> None:
        if self.p.poll() is None:
            self.p.send_signal(signal.SIGKILL if kill else signal.SIGTERM)
        try:
            self.p.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.p.kill()
            self.p.wait()
            raise AssertionError('Collector failed to terminate')
        finally:
            self.log.close()
        if not kill:
            assert self.p.returncode == 0, f'Collector shutdown exit {self.p.returncode}'


def run_case(binary: Path, collector: Path, root: Path, sig: str, ct: str, wal_gzip: bool) -> dict:
    root.mkdir(parents=True)
    (root / 'state').mkdir(mode=0o700)
    (root / 'case.json').write_text(json.dumps({'signal': sig, 'content_type': ct, 'wal_gzip': wal_gzip}))
    mu, gate = threading.Lock(), threading.Event()
    servers, threads, collectors, applications = [], [], [], []
    errors = []
    # Exclude duplicate ephemeral ports selected before their listeners start.
    ports = set()
    while len(ports) < 4:
        ports.add(port())
    listen, management, sink_port, source_port = sorted(ports)
    base, mgmt = f'http://127.0.0.1:{listen}', f'http://127.0.0.1:{management}'
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def peer(hop: str, target: str = '') -> str:
        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = 'HTTP/1.1'

            def log_message(self, *_):
                pass

            def do_POST(self):
                try:
                    body = read_body(self)
                    content_type = self.headers.get('Content-Type', '')
                    encoding = self.headers.get('Content-Encoding', '')
                    status, response = 200, b'{}'
                    if hop == 'egress' and not gate.is_set():
                        status, response = 503, b''
                    elif target:
                        headers = {k: self.headers[k] for k in ('Content-Type', 'Content-Encoding', 'Authorization') if k in self.headers}
                        req = urllib.request.Request(target + self.path, data=body, headers=headers)
                        try:
                            upstream = opener.open(req, timeout=5)
                        except urllib.error.HTTPError as error:
                            upstream = error
                        with upstream:
                            status, response = upstream.code, upstream.read()
                    with mu:
                        dump(root / 'wire.jsonl', {'hop': hop, 'path': self.path, 'content_type': content_type,
                                                 'encoding': encoding, 'body': base64.b64encode(body).decode(), 'status': status})
                    self.send_response(status)
                    self.send_header('Content-Type', content_type)
                    self.send_header('Content-Length', str(len(response)))
                    self.end_headers()
                    self.wfile.write(response)
                except (BrokenPipeError, ConnectionResetError):
                    pass
                except BaseException as error:
                    with mu:
                        errors.append(repr(error))
                    self.close_connection = True
        server = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        server.daemon_threads = True
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        servers.append(server)
        threads.append(thread)
        return f'http://127.0.0.1:{server.server_port}'

    def counters():
        return json.loads(request(mgmt + '/monitor')[1])['otlp']

    try:
        terminal = peer('sink')
        collectors.append(Collector(collector, root, 'sink-collector', sink_port, terminal, 'json', 'none'))
        egress = peer('egress', f'http://127.0.0.1:{sink_port}')
        obj, cfg = config(root, listen, management, egress, wal_gzip)
        obj['settings']['otlp'].update({'max_wal_bytes': 16384, 'destinations': [{
            'id': 'real-collector', **{s + '_endpoint': egress + '/v1/' + s for s in SIGNALS},
            'gzip': True, 'max_attempts': 1, 'timeout': '2s'}]})
        cfg.path.write_text(json.dumps(obj, indent=2))

        def start(number):
            process = Process(binary, cfg, root, number, TOKEN)
            applications.append(process)
            until(lambda: request(mgmt + '/health')[0] == 200, 'application not ready')
            assert process.p.poll() is None, 'application exited during startup'
            dump(root / 'lifecycle.jsonl', {'phase': number, 'event': 'start', 'pid': process.p.pid,
                                            'generation_sha256': digest((root / 'state/generation.json').read_bytes())})
            return process

        def stop(process, number, kill=False):
            assert process.p.poll() is None, 'application exited before the requested signal'
            process.stop(kill=kill)
            dump(root / 'lifecycle.jsonl', {'phase': number, 'event': 'stop', 'pid': process.p.pid,
                                            'exit_code': process.p.returncode})
            assert process.p.returncode == (-signal.SIGKILL if kill else 0), 'unexpected process exit'

        app = start(1)
        ingress = peer('ingress', base)
        collectors.append(Collector(collector, root, 'source-collector', source_port, ingress,
                                    'json' if ct == 'application/json' else 'proto', 'gzip', TOKEN))
        source = f'http://127.0.0.1:{source_port}/v1/{sig}'
        refused = json.loads(payload(sig, 'application/json', 99))
        item = one_item(refused, sig)
        if sig == 'logs':
            item['body']['stringValue'] = 'storage-refusal-' + 'x' * 20000
        else:
            item['name'] = 'storage-refusal-' + 'x' * 20000
        raw = json.dumps(refused).encode()
        status, _ = request(source, raw, 'application/json')
        dump(root / 'source.jsonl', {'sequence': 99, 'body': base64.b64encode(raw).decode(), 'status': status})
        assert status != 200, 'Collector masked storage refusal'
        assert any(r['hop'] == 'ingress' and r['status'] == 503 for r in records(root / 'wire.jsonl')), 'not a WAL admission refusal'

        def send(seq):
            raw = payload(sig, ct, seq)
            status, _ = request(source, raw, ct, compressed=True)
            dump(root / 'source.jsonl', {'sequence': seq, 'body': base64.b64encode(raw).decode(), 'status': status})
            assert status == 200, 'Collector failed to send valid input'

        send(1)
        until(lambda: any(r['hop'] == 'egress' and r['status'] == 503 for r in records(root / 'wire.jsonl')), 'no retry attempt')
        generation = (root / 'state/generation.json').read_bytes()
        stop(app, 1, kill=True)
        gate.set()
        app = start(2)
        until(lambda: counters()['acceptedEnvelopes'] == 1, 'replay did not reach real Collector')
        assert (root / 'state/generation.json').read_bytes() == generation, 'namespace changed'
        stop(app, 2, kill=True)
        app = start(3)
        send(2)
        until(lambda: counters()['acceptedEnvelopes'] == 1, 'new request did not reach real Collector')
        assert (root / 'state/generation.json').read_bytes() == generation, 'namespace changed'
        stop(app, 3)
        for proc in reversed(collectors):
            proc.stop()
        assert not errors, f'recording peer errors: {errors}'
        result = audit(root)
        # Independent negative controls must fail their intended assertions,
        # not merely raise a parser error, crash, or time out.
        controls = []
        for name, filename, expected_error in (
            ('changed-export', 'wire.jsonl', 'application changed or invented wire payload'),
            ('missing-terminal', 'wire.jsonl', 'missing or duplicate terminal records'),
            ('false-crash', 'lifecycle.jsonl', 'crash or graceful exit not observed'),
        ):
            path = root / filename
            original = path.read_bytes()
            rows = records(path)
            if name == 'changed-export':
                row = next(r for r in rows if r['hop'] == 'egress' and r['status'] == 200)
                row['body'] = base64.b64encode(gzip.compress(b'corrupted')).decode()
            elif name == 'missing-terminal':
                rows.remove(next(r for r in rows if r['hop'] == 'sink'))
            else:
                next(r for r in rows if r['event'] == 'stop')['exit_code'] = 0
            path.write_text('\n'.join(json.dumps(r) for r in rows) + '\n')
            try:
                try:
                    audit(root)
                except AssertionError as error:
                    assert str(error) == expected_error, f'{name}: wrong detection: {error}'
                    controls.append({'name': name, 'rejected_by': str(error)})
                else:
                    raise AssertionError(f'auditor accepted {name}')
            finally:
                path.write_bytes(original)
            assert audit(root) == result, 'restored positive control failed'
        (root / 'negative-controls.json').write_text(json.dumps(controls, indent=2))
        (root / 'audit.json').write_text(json.dumps(result, indent=2))
        return result
    finally:
        for proc in applications + collectors:
            if proc.p.poll() is None:
                proc.stop(kill=True)
        for server in servers:
            server.shutdown()
            server.server_close()
        for thread in threads:
            thread.join(timeout=3)


def audit_campaign(root: Path) -> dict:
    summary = json.loads((root / 'summary.json').read_text())
    assert summary['passed'] is True, 'campaign did not finish'
    expected_cases = [(s, ct, z, f'{s}-{ct.split("/")[-1]}-wal-{int(z)}')
                      for s in SIGNALS for ct in CONTENT_TYPES for z in (False, True)]
    assert [r['case'] for r in summary['cases']] == [r[3] for r in expected_cases], 'incomplete scenario matrix'
    for saved, (sig, ct, wal, name) in zip(summary['cases'], expected_cases):
        case = root / name
        assert json.loads((case / 'case.json').read_text()) == {
            'signal': sig, 'content_type': ct, 'wal_gzip': wal}, 'scenario metadata changed'
        assert saved == {'case': name, **audit(case)}, 'summary does not match saved evidence'
        controls = json.loads((case / 'negative-controls.json').read_text())
        assert [c['name'] for c in controls] == ['changed-export', 'missing-terminal', 'false-crash'], 'missing negative controls'
    return {'cases': len(expected_cases), **{
        key: sum(row[key] for row in summary['cases']) for key in
        ('accepted_envelopes', 'terminal_items', 'wal_refusals', 'retry_attempts', 'sigkills')}}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path)
    parser.add_argument('--collector', type=Path)
    parser.add_argument('--artifacts', type=Path, required=True)
    parser.add_argument('--audit-only', action='store_true', help='Reconcile a completed campaign without starting any processes')
    args = parser.parse_args()
    root = args.artifacts.resolve()
    if args.audit_only:
        print(json.dumps(audit_campaign(root), indent=2))
        return
    if args.binary is None or args.collector is None:
        parser.error('--binary and --collector are required unless --audit-only is used')
    binary, collector = args.binary.resolve(), args.collector.resolve()
    root.mkdir(parents=True, exist_ok=True)
    summary = {'passed': False, 'binary_sha256': digest(binary.read_bytes()),
               'collector_sha256': digest(collector.read_bytes()),
               'collector_version': subprocess.check_output([str(collector), '--version'], text=True).strip(), 'cases': []}
    try:
        for sig in SIGNALS:
            for ct in CONTENT_TYPES:
                for compressed_wal in (False, True):
                    name = f'{sig}-{ct.split("/")[-1]}-wal-{int(compressed_wal)}'
                    result = run_case(binary, collector, root / name, sig, ct, compressed_wal)
                    summary['cases'].append({'case': name, **result})
                    print(name, 'PASS', flush=True)
        summary['passed'] = True
    finally:
        (root / 'summary.json').write_text(json.dumps(summary, indent=2))
    print(json.dumps(audit_campaign(root), indent=2))


if __name__ == '__main__':
    main()
