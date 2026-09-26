"""Offline regression tests for the journal-adoption oracle and control setup."""
import json
import os
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest import mock
import zipfile

import verify_journal_adoption as adoption


def test_rows(outcome):
    rows = [{'Action': 'run', 'Test': adoption.TEST_NAME}]
    for name in sorted(adoption.EXPECTED_CASES):
        rows.append({'Action': 'run', 'Test': name})
        if outcome == 'fail':
            message = ('selective replay changed sequence semantics:' if name.endswith('/missing-data')
                       else 'missing ID changed ACK suppression:')
            rows.append({'Action': 'output', 'Test': name, 'Output': message+' got sentinel\n'})
        rows.append({'Action': outcome, 'Test': name})
    rows += [{'Action': outcome, 'Test': adoption.TEST_NAME},
             {'Action': outcome, 'Package': 'gofluentd/internal/controller'}]
    return rows


def as_log(rows):
    return '\n'.join(json.dumps(row) for row in rows)


class TestCaseOracle(unittest.TestCase):
    def test_accepts_complete_positive_and_intended_negative_controls(self):
        for outcome in ('pass', 'fail'):
            with self.subTest(outcome=outcome):
                self.assertEqual(adoption.check_cases(as_log(test_rows(outcome)), outcome), adoption.EXPECTED_CASES)

    def test_rejects_build_failure_from_missing_sum(self):
        rows = [{'Action': 'build-output', 'Output': 'missing go.sum entry for module providing package'},
                {'Action': 'build-fail', 'ImportPath': adoption.JOURNAL},
                {'Action': 'fail', 'FailedBuild': adoption.JOURNAL, 'Package': 'gofluentd/internal/controller'}]
        with self.assertRaisesRegex(RuntimeError, 'did not compile'):
            adoption.check_cases(as_log(rows), 'fail')

    def test_rejects_missing_repeated_renamed_or_skipped_cases(self):
        leaf = sorted(adoption.EXPECTED_CASES)[0]
        for mutation in ('missing', 'duplicate-run', 'duplicate-outcome', 'renamed', 'skipped'):
            with self.subTest(mutation=mutation):
                rows = test_rows('pass')
                if mutation == 'missing':
                    rows = [r for r in rows if r.get('Test') != leaf]
                elif mutation == 'duplicate-run':
                    rows.append({'Action': 'run', 'Test': leaf})
                elif mutation == 'duplicate-outcome':
                    rows.append({'Action': 'pass', 'Test': leaf})
                elif mutation == 'renamed':
                    for r in rows:
                        if r.get('Test') == leaf:
                            r['Test'] = leaf.replace('gzip=false', 'gzip=other')
                else:
                    next(r for r in rows if r.get('Test') == leaf and r['Action'] == 'pass')['Action'] = 'skip'
                with self.assertRaises(RuntimeError):
                    adoption.check_cases(as_log(rows), 'pass')

    def test_requires_intended_assertion_in_every_failed_leaf(self):
        for leaf in adoption.EXPECTED_CASES:
            with self.subTest(leaf=leaf):
                rows = test_rows('fail')
                next(r for r in rows if r.get('Test') == leaf and r['Action'] == 'output')['Output'] = 'unrelated error\n'
                with self.assertRaisesRegex(RuntimeError, 'missing intended behavior assertion'):
                    adoption.check_cases(as_log(rows), 'fail')

    def test_rejects_unrelated_failure_and_missing_package_result(self):
        for label in ('other-failure', 'no-package', 'wrong-package-outcome', 'one-leaf-passed'):
            with self.subTest(label=label):
                rows = test_rows('fail')
                if label == 'other-failure':
                    rows.append({'Action': 'fail', 'Test': 'TestUnrelated'})
                elif label == 'no-package':
                    rows = [r for r in rows if 'Package' not in r]
                elif label == 'wrong-package-outcome':
                    rows[-1]['Action'] = 'pass'
                else:
                    next(r for r in rows if r['Action'] == 'fail')['Action'] = 'pass'
                with self.assertRaises(RuntimeError):
                    adoption.check_cases(as_log(rows), 'fail')

    def test_rejects_panic_timeout_and_race_even_with_expected_assertions(self):
        for message in ('panic: abort', 'panic: test timed out', 'WARNING: DATA RACE'):
            with self.subTest(message=message):
                rows = test_rows('fail')+[{'Action': 'output', 'Output': message}]
                with self.assertRaisesRegex(RuntimeError, 'panic, timeout or race'):
                    adoption.check_cases(as_log(rows), 'fail')


class TestControlChecksums(unittest.TestCase):
    def test_requires_both_selected_version_checksums(self):
        metadata = {'Path': adoption.JOURNAL, 'Version': 'v1.0.0', 'Sum': 'h1:content', 'GoModSum': 'h1:mod'}
        good = f'{adoption.JOURNAL} v1.0.0 h1:content\n{adoption.JOURNAL} v1.0.0/go.mod h1:mod\n'
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp)/'control.sum'
            path.write_text(good)
            adoption.verify_control_download(metadata, 'v1.0.0', path)
            for text in ('', good.splitlines()[1]+'\n', good.splitlines()[0]+'\n',
                         good.replace('h1:content', 'h1:wrong'), good.replace('v1.0.0', 'v0.9.0')):
                with self.subTest(text=text):
                    path.write_text(text)
                    with self.assertRaises(RuntimeError):
                        adoption.verify_control_download(metadata, 'v1.0.0', path)
            path.write_text(good)
            for delta in ({'Path': 'other'}, {'Version': 'v0.9.0'}, {'Error': 'network'},
                          {'Replace': {'Dir': 'local'}}, {'Sum': ''}, {'GoModSum': ''}):
                with self.subTest(delta=delta), self.assertRaises(RuntimeError):
                    adoption.verify_control_download({**metadata, **delta}, 'v1.0.0', path)

    def test_manifests_retained_when_control_setup_fails(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); out = root/'out'; out.mkdir(); controls = root/'controls'; controls.mkdir()
            snapshots = {'go.mod': b'module example.invalid/control\n', 'go.sum': b'original\n'}
            with mock.patch.object(adoption, 'command', return_value=''), \
                 mock.patch.object(adoption, 'download_control', side_effect=RuntimeError('setup failed')):
                with self.assertRaisesRegex(RuntimeError, 'setup failed'):
                    adoption.run_control(root, out, controls, snapshots, {}, 'merged', 'v1.0.0', 1, 'fail')
            self.assertEqual((out/'merged.mod').read_bytes(), snapshots['go.mod'])
            self.assertEqual((out/'merged.sum').read_bytes(), snapshots['go.sum'])

    @unittest.skipUnless(shutil.which('go'), 'Go required for the offline module-proxy fixture')
    def test_cached_module_checksum_written_to_the_control_sum(self):
        # Synthetic file:// proxy only: the real adoption never disables sumdb.
        module = 'example.invalid/adoptioncontrol'; version = 'v1.0.0'
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp); proxy = root/'proxy'; versions = proxy/module/'@v'; versions.mkdir(parents=True)
            gomod = f'module {module}\n\ngo 1.23\n'
            (versions/(version+'.mod')).write_text(gomod)
            (versions/(version+'.info')).write_text(json.dumps({'Version': version, 'Time': '2020-01-01T00:00:00Z'}))
            with zipfile.ZipFile(versions/(version+'.zip'), 'w') as archive:
                archive.writestr(f'{module}@{version}/go.mod', gomod)
                archive.writestr(f'{module}@{version}/value.go', 'package adoptioncontrol\nconst Value = 7\n')
            app = root/'app'; app.mkdir(); out = root/'out'; out.mkdir(); controls = root/'controls'; controls.mkdir()
            (app/'go.mod').write_text('module example.invalid/app\n\ngo 1.23\n')
            (app/'main.go').write_text(f'package app\nimport "{module}"\nvar Value = adoptioncontrol.Value\n')
            snapshots = {name: (app/name).read_bytes() if (app/name).exists() else None for name in ('go.mod', 'go.sum')}
            mod = controls/'merged.mod'; mod.write_text((app/'go.mod').read_text()+f'\nrequire {module} {version}\n')
            mod.with_suffix('.sum').write_text('')
            env = {'GOPROXY': proxy.as_uri(), 'GOSUMDB': 'off', 'GONOSUMDB': '', 'GOPRIVATE': '',
                   'GONOPROXY': '', 'GOWORK': 'off', 'GOTOOLCHAIN': 'local', 'GOFLAGS': '',
                   'GOMODCACHE': str(root/'cache'), 'GOCACHE': str(root/'build-cache')}
            with mock.patch.dict(os.environ, env), mock.patch.object(adoption, 'JOURNAL', module):
                # Prime shared cache outside the application exactly like the
                # resolver; then begin with only a go.mod hash in the control.
                metadata = json.loads(adoption.command(['go', 'mod', 'download', '-json', module+'@'+version],
                                                        app, out/'resolver.json'))
                mod.with_suffix('.sum').write_text(f"{module} {version}/go.mod {metadata['GoModSum']}\n")
                with self.assertRaisesRegex(RuntimeError, 'content checksum'):
                    adoption.verify_control_download(metadata, version, mod.with_suffix('.sum'))
                adoption.download_control(app, out, 'merged', mod, version)
                adoption.verify_control_download(metadata, version, mod.with_suffix('.sum'))
                adoption.command(['go', 'test', '-mod=readonly', '-modfile='+str(mod), './...'], app, out/'test.txt')
            for name, before in snapshots.items():
                self.assertEqual((app/name).read_bytes() if (app/name).exists() else None, before)
            selected = json.loads((out/'merged-journal-download.json').read_text())
            self.assertEqual(selected['Version'], version)


if __name__ == '__main__':
    unittest.main()
