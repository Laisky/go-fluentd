#!/usr/bin/env python3
"""Check README links and execute its exact local-demo commands in isolation.

Only repository-authored, explicitly marked command blocks are executed. No
external Markdown, third-party services, credentials, or Python packages are
used. The native and Docker modes require exclusive use of loopback port 8080.
"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import signal
import socket
import subprocess
import tempfile
import time
import unittest
from urllib.error import HTTPError, URLError
from urllib.parse import unquote, urlsplit
from urllib.request import Request, urlopen

ROOT = Path(__file__).resolve().parents[1]
REQUIRED = {'build', 'run', 'request', 'health', 'docker-build', 'docker-run'}
MARKED = re.compile(r'<!-- readme-check:([a-z-]+) -->\s*```sh\n(.*?)\n```', re.S)
FENCE = re.compile(r'^\s*(`{3,}|~{3,})(.*)$')
LINK = re.compile(r'\]\(([^\s)]+)(?:\s+"[^"]*")?\)')


def prose(text: str) -> str:
    """Remove fenced content, but reject unmatched/malformed closing fences."""
    out = []
    active = None
    for line in text.splitlines():
        match = FENCE.match(line)
        if active is not None:
            if match and match[1][0] == active[0] and len(match[1]) >= len(active) and not match[2].strip():
                active = None
            continue
        if match:
            active = match[1]
        else:
            out.append(line)
    if active is not None:
        raise ValueError('unclosed Markdown code fence')
    return '\n'.join(out)


def anchors(text: str) -> set[str]:
    seen = {}
    result = set()
    for heading in re.findall(r'^#{1,6}\s+(.+?)\s*#*$', prose(text), re.M):
        heading = re.sub(r'<[^>]+>', '', heading).lower().strip()
        slug = re.sub(r'[^\w\- ]', '', heading).replace(' ', '-')
        n = seen.get(slug, 0)
        seen[slug] = n + 1
        result.add(slug + (f'-{n}' if n else ''))
    return result


def blocks(text: str) -> dict[str, str]:
    prose(text)
    pairs = MARKED.findall(text)
    result = dict(pairs)
    if len(pairs) != len(result) or set(result) != REQUIRED:
        raise ValueError('missing, extra or duplicate README executable blocks')
    return result


def check_links(path: Path, root: Path) -> None:
    for target in LINK.findall(prose(path.read_text())):
        parts = urlsplit(target)
        if parts.scheme or parts.netloc:
            if parts.scheme not in ('https', 'http', 'mailto'):
                raise ValueError(f'unsupported link scheme: {target}')
            continue  # External endpoints are not an offline correctness oracle.
        destination = (path.parent / unquote(parts.path)).resolve() if parts.path else path.resolve()
        if not destination.is_relative_to(root.resolve()) or not destination.exists():
            raise ValueError(f'{path.name}: missing/out-of-repository link: {target}')
        if parts.fragment and destination.is_file() and destination.suffix.lower() == '.md':
            if unquote(parts.fragment) not in anchors(destination.read_text()):
                raise ValueError(f'{path.name}: missing anchor: {target}')


def static() -> dict[str, str]:
    readme = ROOT / 'README.md'
    commands = blocks(readme.read_text())
    for path in (readme, ROOT / 'docs/quickstart.md'):
        check_links(path, ROOT)
    for name, script in commands.items():
        subprocess.run(['bash', '-n'], input=script, text=True, check=True)
    return commands


def run(script: str, cwd: Path, timeout: int = 300) -> str:
    result = subprocess.run(['bash', '-eu', '-o', 'pipefail', '-c', script], cwd=cwd,
                            capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f'command failed ({result.returncode}):\n{script}\n{result.stdout}\n{result.stderr}')
    return result.stdout


def get(path: str) -> tuple[int, bytes]:
    with urlopen('http://127.0.0.1:8080' + path, timeout=2) as response:
        return response.status, response.read()


def smoke(mode: str, commands: dict[str, str]) -> None:
    evidence = ROOT / 'var/readme-check' / mode
    evidence.mkdir(parents=True, exist_ok=True)
    # Fail before launching anything rather than killing another user's service.
    with socket.socket() as probe:
        probe.bind(('127.0.0.1', 8080))
    if mode == 'docker':
        existing = run("docker ps -aq --filter 'name=^/go-fluentd-demo$'", ROOT)
        if existing.strip():
            raise RuntimeError('container go-fluentd-demo already exists; stop/remove your demo first')
    setup = commands['build' if mode == 'native' else 'docker-build']
    (evidence / 'build.log').write_text(run(setup, ROOT, timeout=600))
    report = {'mode': mode, 'passed': False, 'checks': []}
    proc = None
    workspace = tempfile.TemporaryDirectory(prefix='go-fluentd-readme-')
    try:
        work = Path(workspace.name)
        (work / 'docs/settings').mkdir(parents=True)
        shutil.copy2(ROOT / 'docs/settings/quickstart.yml', work / 'docs/settings/quickstart.yml')
        if mode == 'native':
            (work / 'build').mkdir()
            shutil.copy2(ROOT / 'build/go-fluentd', work / 'build/go-fluentd')
        launch = commands['run' if mode == 'native' else 'docker-run']
        program = './build/go-fluentd' if mode == 'native' else 'docker run'
        if launch.count('\n' + program) != 1:
            raise ValueError('unexpected demo command layout')
        launch = launch.replace('\n' + program, '\nexec ' + program, 1)
        with (evidence / 'application.log').open('w') as log:
            proc = subprocess.Popen(['bash', '-eu', '-o', 'pipefail', '-c', launch], cwd=work,
                                    stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
            deadline = time.monotonic() + 90
            while True:
                if proc.poll() is not None:
                    raise RuntimeError('application exited before readiness')
                try:
                    if get('/health') == (200, b'hello, world'):
                        break
                except (OSError, URLError):
                    pass
                if time.monotonic() >= deadline:
                    raise TimeoutError('demo did not become healthy')
                time.sleep(.1)
            report['checks'].append('documented startup and health response')
            reply = run(commands['request'], work, timeout=20)
            response = json.loads(reply)
            if type(response.get('msgid')) is not int:
                raise AssertionError('missing numeric local-acceptance receipt')
            report['response'] = response
            report['checks'].append('exact README request returns a numeric receipt')
            deadline = time.monotonic() + 10
            while True:
                text = (evidence / 'application.log').read_text()
                if any('consume msg' in line and 'readme-demo-001' in line
                       and 'hello from the README' in line and 'nested__source:quickstart' in line
                       for line in text.splitlines()):
                    break
                if time.monotonic() >= deadline or proc.poll() is not None:
                    raise AssertionError('console did not receive the documented payload')
                time.sleep(.05)
            report['checks'].append('console sees exact event and flattened nested field')
            state = work / 'var/go-fluentd/journal'
            if not any(p.is_file() and '.buf' in p.name and p.stat().st_size > 0 for p in state.rglob('*')):
                raise AssertionError('demo journal has no nonempty data segment')
            report['checks'].append('real nonempty journal under documented state path')
            (evidence / 'health-monitor.log').write_text(run(commands['health'], work, timeout=10))
            status, body = get('/monitor')
            metrics = json.loads(body)
            if status != 200 or not {'producer', 'journal', 'ts'} <= metrics.keys():
                raise AssertionError('monitor example is not the documented JSON view')
            if get('/pprof/')[0] != 200:
                raise AssertionError('documented profiling route is absent')
            report['checks'].append('health, JSON monitor and pprof routes')
            bad = json.loads((work / 'var/quickstart/request.json').read_text())
            bad.update(event='readme-invalid', sig='invalid')
            request = Request('http://127.0.0.1:8080/ingest/sit', data=json.dumps(bad).encode(),
                              headers={'Content-Type': 'application/json'})
            try:
                with urlopen(request, timeout=3) as response:
                    response.read()
                raise AssertionError('invalid signature was accepted')
            except HTTPError as error:
                if error.code != 400:
                    raise
                error.close()
            report['checks'].append('invalid signature returns HTTP 400')
            if proc.poll() is not None:
                raise AssertionError('demo stopped unexpectedly')
            report['passed'] = True
    finally:
        # Only remove the container created by this test, never an existing one.
        if proc is not None:
            if mode == 'docker':
                subprocess.run(['docker', 'stop', '--time=2', 'go-fluentd-demo'], capture_output=True, timeout=15)
            if proc.poll() is None:
                os.killpg(proc.pid, signal.SIGTERM)
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    os.killpg(proc.pid, signal.SIGKILL)
                    proc.wait(timeout=5)
        workspace.cleanup()
        (evidence / 'results.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report))


class CheckerTests(unittest.TestCase):
    def test_fences(self):
        self.assertEqual(prose('# A\n```sh\nignored\n```'), '# A')
        with self.assertRaises(ValueError):
            prose('```sh\nnever closed')

    def test_anchor_duplicates(self):
        self.assertEqual(anchors('# Run\n## Run\n## `Sync()` and safety'), {'run', 'run-1', 'sync-and-safety'})

    def test_missing_commands(self):
        with self.assertRaises(ValueError):
            blocks('<!-- readme-check:run -->\n```sh\necho hello\n```')

    def test_links(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            page = root / 'README.md'
            page.write_text('# Home\n[valid](#home)\n')
            check_links(page, root)
            for link in ('absent.md', '#absent', '../outside.md'):
                page.write_text(f'# Home\n[bad]({link})\n')
                with self.assertRaises(ValueError):
                    check_links(page, root)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_mutually_exclusive_group(required=True)
    for mode in ('static', 'native', 'docker', 'self-test'):
        modes.add_argument('--' + mode, action='store_true')
    args = parser.parse_args()
    if args.self_test:
        result = unittest.TextTestRunner(verbosity=2).run(unittest.defaultTestLoader.loadTestsFromTestCase(CheckerTests))
        return 0 if result.wasSuccessful() else 1
    commands = static()
    if args.static:
        print('README/quickstart links, anchors, fences and six shell blocks passed')
    else:
        smoke('native' if args.native else 'docker', commands)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
