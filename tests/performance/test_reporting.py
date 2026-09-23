import unittest
from compare import parse, summarize
from pipeline import percentile


class ReportingTests(unittest.TestCase):
    def test_units_and_normalization(self):
        raw = 'BenchmarkPerfExample-2 100 2000 ns/op 64 msgs/op 32000000 msgs/s 80 B/op 2 allocs/op\n'
        data = parse(raw)
        row = summarize(data, data, 1)['BenchmarkPerfExample']
        self.assertEqual(row['after']['messages_per_second'], 32000000)
        self.assertEqual(row['time_change_percent'], 0)

    def test_missing_and_malformed_fail_closed(self):
        for bad in ('PASS\n', 'BenchmarkPerfExample-2 0 0 ns/op\n',
                    'BenchmarkPerfExample-2 3 not-a-number ns/op\n',
                    'BenchmarkPerfExample-2 1 nan ns/op 1 msgs/op 1 msgs/s 0 B/op 0 allocs/op'):
            with self.assertRaises(ValueError):
                parse(bad)
        a = parse('BenchmarkPerfExample-2 100 2 ns/op 1 msgs/op 5 msgs/s 0 B/op 0 allocs/op')
        with self.assertRaises(ValueError):
            summarize(a, {}, 1)
        with self.assertRaises(ValueError):
            summarize(a, a, 2)
        b = parse('BenchmarkPerfExample-2 100 2 ns/op 2 msgs/op 5 msgs/s 0 B/op 0 allocs/op')
        with self.assertRaises(ValueError):
            summarize(a, b, 1)

    def test_latency_nearest_rank(self):
        self.assertEqual(percentile([3, 1, 2, 4], .5), 2)
        self.assertEqual(percentile([3, 1, 2, 4], .99), 4)
