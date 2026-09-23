#!/usr/bin/env python3
"""Closed-loop durable HTTP throughput, checked against two independent disk ledgers.

Input encoding and manifest fsync happen before timing. Sink decoding and fsync
are included. This is a local protocol-peer workload, not a cluster capacity or
open-loop tail-latency claim. No internal application counter proves delivery.
"""
from __future__ import annotations
import argparse
import concurrent.futures
import hashlib
import http.client
import json
import math
import os
from pathlib import Path
import socket
import sys
import threading
import time

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'delivery'))
from test_delivery import App, Ledger, SALT, events, verify_manifest, wait_for


def percentile(values, q):
    ordered = sorted(values)
    return ordered[max(0, math.ceil(len(ordered) * q) - 1)]


def process_stats(pid):
    fields = Path(f'/proc/{pid}/stat').read_text().rsplit(')', 1)[1].split()
    cpu = (int(fields[11]) + int(fields[12])) / os.sysconf('SC_CLK_TCK')
    status = Path(f'/proc/{pid}/status').read_text().splitlines()
    peak = next(int(line.split()[1]) for line in status if line.startswith('VmHWM:'))
    return cpu, peak


def sample(binary, root, count, concurrency, compressed, group_max_messages=None):
    root.mkdir(parents=True, exist_ok=False)
    sinks = [Ledger(root, f'sink-{i}') for i in range(2)]
    # Avoid measuring the Python peer's Nagle/delayed-ACK interaction as Go cost.
    for sink in sinks:
        handler = sink.server.RequestHandlerClass
        original = handler.setup
        def setup(self, original=original):
            original(self)
            self.connection.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        handler.setup = setup
    app = App(root, sinks, compressed=compressed, queue=max(count * 2, 4096), durable=True)
    def overrides(cfg):
        cfg['journal']['buf_file_bytes'] = 64 << 20
        cfg['journal']['committed_id_sec'] = 3600
        if group_max_messages is not None:
            cfg['journal']['group_commit_max_messages'] = group_max_messages
        for sender in cfg['producer']['plugins'].values():
            sender['msg_batch_size'] = 64
    app.overrides = overrides
    os.environ['GO_FLUENTD_BINARY'] = str(binary)
    responses = []
    try:
        app.start()
        warm = events('warm', 1)[0]
        status, _ = app.send(warm)
        if status != 200:
            raise AssertionError(f'warmup rejected: {status}')
        wait_for(lambda: all(len(s.keys()) == 1 for s in sinks), 'warmup did not reach sinks')
        expected = events('perf', count)
        for item in expected:
            item['payload'] = (item['payload'] + ' representative payload 0123456789 ' * 70)[:2048]
        stamp = time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime())
        signature = hashlib.md5((stamp + SALT).encode()).hexdigest()
        bodies = [json.dumps(dict(e, ts=stamp, sig=signature), ensure_ascii=False).encode() for e in expected]
        with (root / 'producer-manifest.jsonl').open('wb') as output:
            for body in bodies:
                output.write(body + b'\n')
            output.flush()
            os.fsync(output.fileno())
        barrier = threading.Barrier(concurrency + 1, timeout=20)
        def worker(index):
            conn = http.client.HTTPConnection('127.0.0.1', app.port, timeout=20)
            local = []
            try:
                conn.connect()
                conn.sock.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
                barrier.wait()
                for n in range(index, count, concurrency):
                    start = time.perf_counter_ns()
                    conn.request('POST', '/ingest/prod', bodies[n], {'Content-Type': 'application/json'})
                    response = conn.getresponse()
                    reply = response.read()
                    end = time.perf_counter_ns()
                    local.append({'event': expected[n]['event'], 'status': response.status,
                                  'latency_ms': (end - start) / 1e6, 'finish_ns': end})
                    if response.status != 200:
                        raise AssertionError(f'not durably accepted: {response.status}: {reply!r}')
            finally:
                conn.close()
            return local
        cpu_before, _ = process_stats(app.proc.pid)
        with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as executor:
            futures = [executor.submit(worker, i) for i in range(concurrency)]
            start_ns = time.perf_counter_ns()
            barrier.wait()
            for future in futures:
                responses.extend(future.result(timeout=90))
        accepted_seconds = (max(r['finish_ns'] for r in responses) - start_ns) / 1e9
        deadline = time.monotonic() + 60
        while not all(len(s.keys()) == count + 1 for s in sinks):
            if time.monotonic() >= deadline:
                raise AssertionError(f'missing sink delivery: {[len(s.keys()) for s in sinks]}')
            time.sleep(.001)
        delivered_seconds = (time.perf_counter_ns() - start_ns) / 1e9
        cpu_after, peak_kib = process_stats(app.proc.pid)
        for sink in sinks:
            if sink.errors:
                raise AssertionError(sink.errors)
            # Verify the actual persisted ledger, not just its in-memory count.
            received = [json.loads(line) for line in sink.path.read_text().splitlines()]
            verify_manifest([warm] + expected, received)
            if len(received) != count + 1:
                raise AssertionError('duplicate delivery in failure-free performance run')
        latencies = [r['latency_ms'] for r in responses]
        result = {'count': count, 'concurrency': concurrency, 'gzip': compressed,
                  'durable_ack': True, 'group_commit_max_messages': group_max_messages, 'payload_characters': 2048, 'sink_batch': 64,
                  'mean_wire_bytes': sum(map(len, bodies)) / count,
                  'accepted': len(responses), 'delivered_per_sink': [len(s.keys()) - 1 for s in sinks],
                  'accepted_seconds': accepted_seconds, 'delivered_seconds': delivered_seconds,
                  'accepted_per_second': count / accepted_seconds,
                  'delivered_per_second': count / delivered_seconds,
                  'latency_ms': {'p50': percentile(latencies, .5), 'p95': percentile(latencies, .95),
                                 'p99': percentile(latencies, .99), 'max': max(latencies)},
                  'app_cpu_seconds': cpu_after - cpu_before, 'app_peak_rss_kib': peak_kib}
        (root / 'requests.json').write_text(json.dumps(responses))
        (root / 'sample.json').write_text(json.dumps(result, indent=2))
        return result
    finally:
        try:
            app.crash()
        finally:
            for sink in sinks:
                sink.close()
        for log in root.glob('process-*.log'):
            if any(m in log.read_text(errors='replace') for m in ('WARNING: DATA RACE', 'panic:', 'fatal error:')):
                raise AssertionError(f'application diagnostic failure: {log}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--count', type=int, default=1024)
    parser.add_argument('--repeats', type=int, default=3)
    parser.add_argument('--concurrency', type=int, nargs='+', default=[1, 16])
    args = parser.parse_args()
    if args.count < 64 or args.count % 64 or args.repeats < 1 or min(args.concurrency) < 1:
        parser.error('count must be a positive multiple of 64; concurrency/repeats must be positive')
    binary = args.binary.resolve(strict=True)
    args.output.mkdir(parents=True, exist_ok=False)
    records = []
    for repeat in range(args.repeats):
        for compressed in (False, True):
            for concurrency in args.concurrency:
                name = f'repeat-{repeat}-gzip-{compressed}-c-{concurrency}'
                result = sample(binary, args.output / name, args.count, concurrency, compressed)
                records.append(result)
                print(name, json.dumps(result), flush=True)
    report = {'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(), 'samples': records,
              'method': 'closed-loop; latency from request write to durable HTTP response; two fsynced local peers'}
    (args.output / 'pipeline.json').write_text(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
