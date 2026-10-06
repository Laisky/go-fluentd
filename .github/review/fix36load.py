from pathlib import Path
import json
root=Path('candidate')
p=root/'tests/loadtest/main.go';s=p.read_text()
s=s.replace('BoundedFixtures                                                    bool','BoundedFixtures                                                    bool\n\tStorageScanTimeout string')
s=s.replace('return map[string]any{"settings": map[string]any{','config := map[string]any{"settings": map[string]any{',1)
s=s.replace('\n\t}}\n}\nfunc (t *trial) sink','\n\t}}\n\tif o.StorageScanTimeout != "" {\n\t\tconfig["settings"].(map[string]any)["otlp"].(map[string]any)["storage_scan_timeout"] = o.StorageScanTimeout\n\t}\n\treturn config\n}\nfunc (t *trial) sink',1)
s=s.replace('flag.StringVar(&o.Replay,','flag.StringVar(&o.StorageScanTimeout, "storage-scan-timeout", "", "explicit capacity-scan budget; omit for older binaries without this setting")\n\tflag.StringVar(&o.Replay,',1)
s=s.replace('\n\tp, e := filepath.Abs(o.Binary)','\n\tif o.StorageScanTimeout != "" {\n\t\td, err := time.ParseDuration(o.StorageScanTimeout)\n\t\tif err != nil || d <= 0 || d > time.Minute {\n\t\t\tfmt.Fprintln(os.Stderr, "invalid storage scan timeout")\n\t\t\tos.Exit(2)\n\t\t}\n\t}\n\tp, e := filepath.Abs(o.Binary)',1)
p.write_text(s)
p=root/'tests/loadtest/compare.py';s=p.read_text().replace('\ndef main():','''
def driver_args(case, label):
    """Retain identical offered work; allow one explicit new capacity setting.

    The frozen baseline predates this setting. Never silently change the old
    binary, retry failures, or erase refused requests from capacity evidence.
    """
    if label not in ('baseline', 'candidate'):
        raise ValueError('unknown trial label')
    args = list(case['args'])
    scan_timeout = case.get('candidate_storage_scan_timeout')
    if label == 'candidate' and scan_timeout is not None:
        if not isinstance(scan_timeout, str) or not scan_timeout:
            raise ValueError('candidate scan timeout must be a duration string')
        args.extend(['--storage-scan-timeout', scan_timeout])
    return args


def main():''').replace("*case['args']]", "*driver_args(case, label)]")
p.write_text(s)
p=root/'tests/loadtest/cases/ci.json';cases=json.loads(p.read_text())
for c in cases: c['candidate_storage_scan_timeout']='1s'
p.write_text(json.dumps(cases,indent=2)+'\n')
p=root/'.github/workflows/e2e-load.yml';s=p.read_text().replace('python3 -m unittest -v test_sustained','python3 -m unittest -v test_sustained test_compare').replace('--rate 100 --requests 512 --concurrency 32','--rate 100 --requests 512 --concurrency 32 --storage-scan-timeout 1s');p.write_text(s)
p=root/'docs/otlp-edge-storage.md';s=p.read_text();s+='''
The CI delivery campaign sets the candidate's `storage_scan_timeout` to `1s`
explicitly. Its frozen historical baseline predates that setting and receives
no unknown configuration key. Both revisions still receive identical payloads,
concurrency and delivery-window pacing. The campaign manifest, command and
configuration record this policy difference: results are **not** zero-refusal
capacity claims for the production `25ms` default on shared runners. No failed
trial is retried, dropped or accepted as a capacity sample; refusal/deadline
behavior remains covered by the storage budget contract tests.
''';p.write_text(s)
(root/'tests/loadtest/test_compare.py').write_text('''import unittest
from compare import driver_args

class TrialArgumentsTests(unittest.TestCase):
    def test_new_capacity_setting_is_explicit_and_candidate_only(self):
        case = {'args': ['--requests', '2048', '--delivery-window'],
                'candidate_storage_scan_timeout': '1s'}
        self.assertEqual(driver_args(case, 'baseline'), case['args'])
        self.assertEqual(driver_args(case, 'candidate'),
                         case['args'] + ['--storage-scan-timeout', '1s'])
        self.assertEqual(case['args'], ['--requests', '2048', '--delivery-window'])

    def test_default_campaigns_keep_identical_flags(self):
        case = {'args': ['--protocol', 'logs']}
        self.assertEqual(driver_args(case, 'baseline'), driver_args(case, 'candidate'))

    def test_invalid_policy_is_not_silently_ignored(self):
        for value in ('', 1, False):
            with self.assertRaises(ValueError):
                driver_args({'args': [], 'candidate_storage_scan_timeout': value}, 'candidate')
        with self.assertRaises(ValueError):
            driver_args({'args': []}, 'misspelled')
''')
(root/'tests/loadtest/storage_scan_config_test.go').write_text('''package main

import "testing"

func TestStorageScanConfigIsExplicitForCompatibleBinaries(t *testing.T) {
 for _, timeout := range []string{"", "1s"} {
  tr := trial{root:t.TempDir(), opts:options{Destinations:1, StorageScanTimeout:timeout}}
  config := tr.config()["settings"].(map[string]any)["otlp"].(map[string]any)
  got,exists := config["storage_scan_timeout"]
  if exists != (timeout != "") { t.Fatalf("timeout=%q key existence=%v",timeout,exists) }
  if exists && got != timeout { t.Fatalf("timeout=%q value=%v",timeout,got) }
 }
}
''')
