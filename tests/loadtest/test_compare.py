import unittest
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
