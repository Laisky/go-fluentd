#!/usr/bin/env python3
"""Exercise CloudEvents, NDJSON and OTLP in one configured executable.

Independent Python wire fixtures are reused, not application codecs. Both WALs
must recover after the same SIGKILL; neither pipeline can satisfy the other's
receipts or sink records. Identical legacy-event retries remain allowed.
"""
from __future__ import annotations

import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
import gzip
import hashlib
import http.server
import importlib.util
import json
import os
from pathlib import Path
import signal
import threading
import urllib.error
import urllib.request


def load(name, relative):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).resolve().parents[1] / relative)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


events = load('event_contract', 'event_formats/run.py')
otlp = load('otlp_contract', 'otlp_service/run.py')
MODES = ('ndjson', 'structured', 'binary', 'batch')
SIGNALS = ('logs', 'metrics', 'traces')
TYPES = ('application/json', 'application/x-protobuf')
TOKEN = 'combined-otlp-fixture-only'


def expected():
    result = {}
    for seq in (1, 2):
        for mode in MODES:
            result[f'{mode}-{seq}'] = events.fixtures(mode, 2, 127)[seq - 1]
        for sig in SIGNALS:
            for ct in TYPES:
                raw = otlp.payload(sig, ct, seq)
                result[f'{sig}-{ct}-{seq}'] = (sig, ct, raw)
    return result


def audit(root):
    want = expected()
    accepted = [json.loads(s) for s in (root / 'accepted.jsonl').read_text().splitlines()]
    assert len(accepted) == 20 and {r['key'] for r in accepted} == set(want), 'incomplete source receipts'
    for row in accepted:
        assert row['status'] == (200 if isinstance(want[row['key']], tuple) else 204), 'false source acceptance'
    lifecycle = json.loads((root / 'lifecycle.json').read_text())
    assert lifecycle['exits'] == [-signal.SIGKILL, 0], 'crash and shutdown not observed'
    assert lifecycle['generations'][0] == lifecycle['generations'][1], 'OTLP generation changed'
    seen, attempts = set(), set()
    rows = [json.loads(s) for s in (root / 'wire.jsonl').read_text().splitlines()]
    for row in rows:
        headers = row['headers']
        raw = base64.b64decode(row['body'], validate=True)
        if row['path'].startswith('/v1/'):
            assert headers.get('content-encoding') == 'gzip', 'OTLP export gzip missing'
            value = (row['path'].split('/')[-1], headers['content-type'], gzip.decompress(raw))
            keys = [k for k, v in want.items() if isinstance(v, tuple) and v == value]
            assert len(keys) == 1, 'OTLP signal, content type or bytes changed'
        else:
            mode = row['path'].removeprefix('/events/')
            assert mode in MODES, 'unknown event route'
            values = events.parse_wire(mode, headers, raw)
            keys = [k for value in values for k, v in want.items()
                    if k.startswith(mode + '-') and events.canonical(v) == events.canonical(value)]
            assert len(keys) == len(values) and keys, 'event content changed or cross-routed'
        assert row['status'] in (200, 204, 503), 'unexpected sink result'
        attempts.update(keys)
        if row['status'] != 503:
            seen.update(keys)
    assert attempts == set(want), 'missing pipeline attempts'
    assert seen == set(want), 'missing accepted delivery'
    assert any(r['status'] == 503 for r in rows), 'outage was not exercised'
    return {'accepted': 20, 'event_records': 8, 'otlp_envelopes': 12, 'sigkills': 1,
            'wire_attempts': len(rows)}


def run_case(binary, root, compressed):
    root.mkdir(parents=True)
    (root / 'otlp').mkdir(mode=0o700)
    lock, gate = threading.Lock(), threading.Event()
    errors, exits, generations = [], [], []

    class Peer(http.server.BaseHTTPRequestHandler):
        protocol_version = 'HTTP/1.1'

        def log_message(self, *_):
            pass

        def do_POST(self):
            try:
                is_otlp = self.path.startswith('/v1/')
                token = TOKEN if is_otlp else events.TOKEN
                assert self.headers.get('Authorization') == 'Bearer ' + token, 'crossed destination credentials'
                size = int(self.headers['Content-Length'])
                assert 0 < size <= 65536, 'sink size bound'
                raw = self.rfile.read(size)
                assert len(raw) == size, 'incomplete sink request'
                status = (200 if is_otlp else 204) if gate.is_set() else 503
                ct = self.headers.get('Content-Type')
                body = b'{}' if is_otlp and status == 200 and ct == 'application/json' else b''
                with lock:
                    otlp.dump(root / 'wire.jsonl', {'path': self.path, 'status': status,
                              'headers': {k.lower(): v for k, v in self.headers.items() if k.lower() != 'authorization'},
                              'body': base64.b64encode(raw).decode()})
                self.send_response(status)
                self.send_header('Content-Type', ct)
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            except (BrokenPipeError, ConnectionResetError):
                pass
            except Exception as exc:
                with lock:
                    errors.append(repr(exc))
                self.close_connection = True

    peer = http.server.ThreadingHTTPServer(('127.0.0.1', 0), Peer)
    peer.daemon_threads = True
    thread = threading.Thread(target=peer.serve_forever, daemon=True)
    thread.start()
    target = f'http://127.0.0.1:{peer.server_port}'
    app = events.App(root, [], compressed=compressed, queue=64)
    listen = otlp.port()
    os.environ['COMBINED_OTLP_TOKEN'] = TOKEN

    def configure(settings):
        assert app.port != listen, 'ephemeral application port collision'
        settings['journal']['group_commit_max_messages'] = 64
        settings['post_filters']['plugins'] = {}
        settings['acceptor']['recvs']['plugins'] = {
            mode: {'type': 'http_events', 'active_env': ['prod'], 'path': '/events/' + mode,
                   'tag': mode + '.{env}', 'format': 'ndjson' if mode == 'ndjson' else 'cloudevents',
                   'bearer_token': events.TOKEN, 'ack_timeout_sec': 5} for mode in MODES}
        settings['producer']['plugins'] = {
            mode: {'type': 'http_events', 'active_env': ['prod'], 'addr': target + '/events/' + mode,
                   'tags': [mode + '.{env}'], 'format': 'ndjson' if mode == 'ndjson' else 'cloudevents',
                   'mode': '' if mode == 'ndjson' else mode, 'bearer_token': events.TOKEN,
                   'msg_batch_size': 1, 'max_attempts': 1, 'request_timeout_sec': 2} for mode in MODES}
        settings['otlp'] = {
            'enabled': True, 'listen_addr': f'127.0.0.1:{listen}', 'storage_dir': str(root / 'otlp'),
            'bearer_token_env': 'COMBINED_OTLP_TOKEN', 'journal_gzip': compressed,
            'replay_interval': '50ms', 'replay_batch': 16, 'destinations': [{
                'id': 'isolated-otlp', **{s + '_endpoint': target + '/v1/' + s for s in SIGNALS},
                'bearer_token_env': 'COMBINED_OTLP_TOKEN', 'gzip': True, 'max_attempts': 1, 'timeout': '2s'}]}

    app.overrides = configure

    def send(key):
        value = expected()[key]
        if isinstance(value, tuple):
            sig, ct, raw = value
            status, _ = otlp.request(f'http://127.0.0.1:{listen}/v1/{sig}', raw, ct, TOKEN, compressed=True)
        else:
            mode = key.split('-')[0]
            raw, headers = events.wire(mode, [value])
            request = urllib.request.Request(f'http://127.0.0.1:{app.port}/events/{mode}', raw, headers)
            with urllib.request.urlopen(request, timeout=8) as response:
                status = response.status
                response.read()
        assert status == (200 if isinstance(value, tuple) else 204), 'input not durably accepted'
        with lock:
            otlp.dump(root / 'accepted.jsonl', {'key': key, 'status': status})

    def observed(seq, successful=True):
        with lock:
            path = root / 'wire.jsonl'
            rows = [json.loads(s) for s in path.read_text().splitlines()] if path.exists() else []
        seen = set()
        want = expected()
        for row in rows:
            if successful and row['status'] == 503:
                continue
            raw = base64.b64decode(row['body'], validate=True)
            if row['path'].startswith('/v1/'):
                value = (row['path'].split('/')[-1], row['headers']['content-type'], gzip.decompress(raw))
                seen.update(k for k, v in want.items() if isinstance(v, tuple) and v == value)
            else:
                mode = row['path'].removeprefix('/events/')
                for value in events.parse_wire(mode, row['headers'], raw):
                    seen.update(k for k, v in want.items() if k.startswith(mode + '-')
                                and events.canonical(v) == events.canonical(value))
        required = {k for k in want if int(k.rsplit('-', 1)[1]) <= seq}
        return seen >= required

    try:
        app.start()
        generations.append(hashlib.sha256((root / 'otlp/generation.json').read_bytes()).hexdigest())
        with ThreadPoolExecutor(max_workers=10) as workers:
            list(workers.map(send, [k for k in expected() if k.endswith('-1')]))
        events.wait_for(lambda: observed(1, successful=False),
                        'both blocked pipelines were not attempted')
        process = app.proc
        app.crash()
        exits.append(process.returncode)
        gate.set()
        app.start()
        generations.append(hashlib.sha256((root / 'otlp/generation.json').read_bytes()).hexdigest())
        events.wait_for(lambda: observed(1), 'combined WAL recovery did not progress')
        with ThreadPoolExecutor(max_workers=10) as workers:
            list(workers.map(send, [k for k in expected() if k.endswith('-2')]))
        events.wait_for(lambda: observed(2), 'new mixed workload did not progress')
        events.wait_for(lambda: json.loads(otlp.request(f'http://127.0.0.1:{app.port}/monitor')[1])['otlp']['acceptedEnvelopes'] == 12,
                        'OTLP outcomes did not become durable')
        app.proc.send_signal(signal.SIGTERM)
        app.proc.wait(timeout=10)
        exits.append(app.proc.returncode)
        app.logfile.close()
        app.proc = None
        assert not errors, errors
        (root / 'lifecycle.json').write_text(json.dumps({'exits': exits, 'generations': generations}))
        result = audit(root)
        original = (root / 'wire.jsonl').read_bytes()
        rows = [json.loads(s) for s in original.decode().splitlines()]
        controls = []
        for prefix in ('/events/', '/v1/'):
            corrupted = [r for r in rows if not (r['path'].startswith(prefix) and r['status'] != 503)]
            (root / 'wire.jsonl').write_text('\n'.join(json.dumps(r) for r in corrupted) + '\n')
            try:
                audit(root)
            except AssertionError as exc:
                assert str(exc) in ('missing pipeline attempts', 'missing accepted delivery'), str(exc)
                controls.append(prefix)
            else:
                raise AssertionError('auditor allowed a missing pipeline')
            finally:
                (root / 'wire.jsonl').write_bytes(original)
            assert audit(root) == result
        (root / 'negative-controls.json').write_text(json.dumps(controls))
        return result
    finally:
        app.crash()
        peer.shutdown()
        peer.server_close()
        thread.join(timeout=3)
        for path in root.glob('process-*.log'):
            text = path.read_text(errors='replace')
            assert not any(x in text for x in ('WARNING: DATA RACE', 'panic:', 'fatal error:')), text[-4000:]


def main():
    if not __debug__:
        raise RuntimeError('do not run assertion-based tests with Python -O')
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path)
    parser.add_argument('--artifacts', type=Path, required=True)
    parser.add_argument('--audit-only', action='store_true')
    args = parser.parse_args()
    root = args.artifacts.resolve()
    if not args.audit_only:
        if not args.binary:
            parser.error('--binary is required')
        os.environ['GO_FLUENTD_BINARY'] = str(args.binary.resolve(strict=True))
        for compressed in (False, True):
            print(run_case(args.binary, root / f'wal-{int(compressed)}', compressed), flush=True)
    results = [audit(root / f'wal-{i}') for i in range(2)]
    print(json.dumps({'cases': 2, 'results': results}, indent=2))


if __name__ == '__main__':
    main()
