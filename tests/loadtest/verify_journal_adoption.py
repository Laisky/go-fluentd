#!/usr/bin/env python3
"""Check immutable journal adoption against old and merged-regression controls.

Only controls use temporary modfiles; the candidate uses committed go.mod/go.sum
without replacements. No application or journal source is patched by this test.
"""
from __future__ import annotations
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tempfile

JOURNAL = 'github.com/Laisky/go-journal'
OLD = 'v1.1.7-0.20260924003054-612f5354f538'
MERGED_BAD = '8758f84c23d9ae29bf800f9e84d2ec64125008aa'
CORRECTED = 'v1.1.7-0.20260926040517-fc156a60fafc'
CORRECTED_SOURCE = '5e16d047cbd5f76791b0f5759bb46c3d26eb09d1'
PATTERN = '^TestRegressionSelectiveReplaySequenceCompatibility$'


def objects(text):
    decoder = json.JSONDecoder()
    values = []
    while text.strip():
        text = text.lstrip()
        value, end = decoder.raw_decode(text)
        values.append(value)
        text = text[end:]
    return values


def command(args, root, destination, expected=0):
    with destination.open('w') as out, destination.with_name(destination.name+'.stderr').open('w') as err:
        result = subprocess.run(args, cwd=root, stdout=out, stderr=err, timeout=600)
    if result.returncode != expected:
        raise RuntimeError(f'{args!r}: exit {result.returncode}, expected {expected}; see {destination}')
    return destination.read_text()


def check_cases(log, expected):
    rows = [json.loads(line) for line in log.splitlines() if line.startswith('{')]
    leaves = {r.get('Test', '') for r in rows if r.get('Action') == expected
              and r.get('Test', '').startswith('TestRegressionSelectiveReplaySequenceCompatibility/')
              and r.get('Test', '').endswith(('/missing-data', '/missing-id'))}
    assert len(leaves) == 8, f'expected eight {expected} cases, found {len(leaves)}'
    assert not any(r.get('Action') == 'skip' for r in rows), 'skipped cases are not acceptance'
    if expected == 'pass':
        assert not any(r.get('Action') == 'fail' for r in rows), 'unexpected failed test'
    else:
        output = ''.join(r.get('Output', '') for r in rows)
        assert 'selective replay changed sequence semantics:' in output, 'missing payload assertion'
        assert 'missing ID changed ACK suppression:' in output, 'missing ID assertion'
    return leaves


def graph(root, out, modfile=None):
    args = ['go', 'list', '-mod=readonly']
    if modfile:
        args += ['-modfile='+str(modfile)]
    args += ['-m', '-json', 'all']
    modules = objects(command(args, root, out))
    assert not any(m.get('Replace') or m.get('Error') for m in modules), 'replacement/incomplete module graph'
    return {m['Path']: m.get('Version', '') for m in modules}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('out', type=Path)
    args = parser.parse_args()
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    root = Path(__file__).resolve().parents[2]
    snapshots = {name: (root/name).read_bytes() for name in ('go.mod', 'go.sum')}
    assert not (root/'go.work').exists(), 'workspace override is not native adoption'
    native = graph(root, out/'candidate-modules.json')
    assert native[JOURNAL] == CORRECTED, 'unexpected corrected dependency'
    metadata = objects(command(['go', 'list', '-mod=readonly', '-m', '-json', JOURNAL], root, out/'candidate-journal.json'))[0]
    source = (Path(metadata['Dir'])/'selective.go').read_bytes()
    assert hashlib.sha1(b'blob '+str(len(source)).encode()+b'\0'+source).hexdigest() == CORRECTED_SOURCE
    command(['go', 'mod', 'verify'], root, out/'module-verify.txt')
    expected_cases = check_cases(command(['go', 'test', '-mod=readonly', '-count=1', '-json', '-run', PATTERN,
                                          './internal/controller'], root, out/'candidate-sequence.jsonl'), 'pass')
    with tempfile.TemporaryDirectory(prefix='journal-adoption-') as temp:
        temp = Path(temp)
        resolver = temp/'resolver'
        resolver.mkdir()
        command(['go', 'mod', 'init', 'example.invalid/journal-adoption'], resolver, out/'resolver-init.txt')
        bad = objects(command(['go', 'mod', 'download', '-json', JOURNAL+'@'+MERGED_BAD], resolver, out/'merged-module.json'))[0]
        assert not bad.get('Error') and bad.get('Sum') and bad.get('GoModSum'), 'unresolved merged control'
        for label, version, status, outcome in [('old', OLD, 0, 'pass'), ('merged', bad['Version'], 1, 'fail')]:
            mod = temp/(label+'.mod')
            mod.write_bytes(snapshots['go.mod'])
            mod.with_suffix('.sum').write_bytes(snapshots['go.sum'])
            command(['go', 'mod', 'edit', '-modfile='+str(mod), '-require='+JOURNAL+'@'+version], root, out/(label+'-edit.txt'))
            command(['go', 'mod', 'download', '-modfile='+str(mod)], root, out/(label+'-download.txt'))
            control_graph = graph(root, out/(label+'-modules.json'), mod)
            assert control_graph.keys() == native.keys(), 'unexpected graph membership change'
            differences = sorted(name for name in native if native[name] != control_graph[name])
            assert differences == [JOURNAL], f'unrelated dependency changes: {differences}'
            log = command(['go', 'test', '-mod=readonly', '-modfile='+str(mod), '-count=1', '-json',
                           '-run', PATTERN, './internal/controller'], root, out/(label+'-sequence.jsonl'), status)
            assert check_cases(log, outcome) == expected_cases, 'different public cases executed'
            shutil.copyfile(mod, out/(label+'.mod'))
            shutil.copyfile(mod.with_suffix('.sum'), out/(label+'.sum'))
    assert all((root/name).read_bytes() == data for name, data in snapshots.items()), 'candidate manifests mutated'
    result = {'candidate_version': CORRECTED, 'changed_module': JOURNAL, 'candidate_replacements': False,
              'old_pass': 8, 'merged_expected_failures': 8, 'corrected_pass': 8,
              'candidate_source_blob': CORRECTED_SOURCE, 'native_module_count': len(native), 'passed': True}
    (out/'adoption.json').write_text(json.dumps(result, indent=2)+'\n')
    print(json.dumps(result, indent=2))


if __name__ == '__main__':
    main()
