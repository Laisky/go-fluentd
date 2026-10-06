"""Independent negative controls for the executable checkpoint oracle."""
import copy
import hashlib
import unittest
from run import assert_generation_progress, validate_generation


def generation(released=0, namespace='ab' * 32):
    return {'version': 2, 'namespace': namespace, 'released_through': released,
            'checksum': hashlib.sha256(f'2:{namespace}:{released}'.encode()).hexdigest()}


class GenerationOracleTests(unittest.TestCase):
    def test_upgrade_and_progress_are_valid(self):
        old = {'version': 1, 'namespace': 'ab' * 32}
        for new in [generation(), generation(1), generation(7)]:
            assert_generation_progress(old, new)
            old = new

    def test_rejects_validly_checksummed_identity_reset(self):
        with self.assertRaisesRegex(AssertionError, 'namespace changed'):
            assert_generation_progress(generation(2), generation(3, 'cd' * 32))

    def test_rejects_validly_checksummed_frontier_rollback(self):
        with self.assertRaisesRegex(AssertionError, 'released frontier regressed'):
            assert_generation_progress(generation(7), generation(6))

    def test_rejects_version_rollback(self):
        with self.assertRaisesRegex(AssertionError, 'generation version regressed'):
            assert_generation_progress(generation(), {'version': 1, 'namespace': 'ab' * 32})

    def test_rejects_corrupt_checkpoint_and_invalid_limits(self):
        g = generation(7)
        for field, value in [('checksum', '00' * 32), ('released_through', -1),
                             ('released_through', 2**63), ('released_through', True),
                             ('namespace', 'AB' * 32), ('version', 3)]:
            with self.subTest(field=field, value=value):
                bad = copy.deepcopy(g); bad[field] = value
                with self.assertRaises(AssertionError):
                    validate_generation(bad)


if __name__ == '__main__':
    unittest.main()
