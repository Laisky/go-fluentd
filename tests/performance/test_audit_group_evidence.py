import copy
import json
from pathlib import Path
import tempfile
import unittest

from audit_group_evidence import audit_sample


class LedgerAuditTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.fixture()

    def fixture(self):
        submitted = [{'event': 'a', 'seq': 1, 'payload': 'first', 'nested': {'origin': 'source'}},
                     {'event': 'b', 'seq': 2, 'payload': 'second', 'nested': {'origin': 'source'}}]
        warm = {'event': 'warm', 'seq': 0, 'payload': 'warm', 'nested': {'origin': 'warm'}}
        received = []
        for i, source in enumerate([warm]+submitted):
            item = copy.deepcopy(source)
            item['nested__origin'] = item.pop('nested')['origin']
            item.update(tag='events.prod', msgid=f'id-{i}')
            received.append(item)
        self.write('producer-manifest.jsonl', submitted)
        self.write('wire-requests.jsonl', [warm])
        self.write('sink-0.jsonl', received)
        self.write('sink-1.jsonl', received)
        self.write('requests.json', [{'event': 'a', 'status': 200, 'latency_ms': 1},
                                     {'event': 'b', 'status': 200, 'latency_ms': 2}])
        self.write('sample.json', {'count': 2, 'accepted': 2, 'delivered_per_sink': [2,2],
                   'durable_ack': True, 'gzip': False, 'group_commit_max_messages': 64,
                   'sink_batch': 64, 'accepted_seconds': .02, 'delivered_seconds': .03,
                   'accepted_per_second': 100, 'delivered_per_second': 2/.03,
                   'latency_ms': {'p50': 1, 'p95': 2, 'p99': 2, 'max': 2}})
        self.write('config.json', {'settings': {'journal': {'is_compress': False, 'group_commit_max_messages': 64},
                   'acceptor': {'recvs': {'plugins': {'http': {'require_durable_ack': True}}}},
                   'producer': {'plugins': {'a': {'msg_batch_size': 64, 'is_discard_when_blocked': False},
                                           'b': {'msg_batch_size': 64, 'is_discard_when_blocked': False}}}}})

    def write(self, name, value):
        text = ''.join(json.dumps(x)+'\n' for x in value) if name.endswith('.jsonl') else json.dumps(value)
        (self.root/name).write_text(text)

    def read(self, name):
        text = (self.root/name).read_text()
        return [json.loads(x) for x in text.splitlines()] if name.endswith('.jsonl') else json.loads(text)

    def test_complete_independent_ledgers(self):
        result = audit_sample(self.root, 2)
        self.assertEqual((result['accepted'], result['sink_deliveries']), (2,4))
        self.assertEqual(len(result['sha256']), 5)

    def test_missing_duplicate_and_fabricated_events_fail(self):
        source = self.read('sink-1.jsonl')
        for bad in [source[:-1], source+source[:1], source+[dict(source[0], event='fabricated')]]:
            with self.subTest(records=bad):
                self.write('sink-1.jsonl', bad)
                with self.assertRaises(ValueError):
                    audit_sample(self.root, 2)

    def test_payload_types_and_cross_sink_identity_are_checked(self):
        source = self.read('sink-1.jsonl')
        for change in [{'seq': True}, {'payload': 'corrupted'}, {'msgid': 'different-id'}, {'extra': 'hidden'}]:
            with self.subTest(change=change):
                records = copy.deepcopy(source)
                records[1].update(change)
                self.write('sink-1.jsonl', records)
                with self.assertRaises(ValueError):
                    audit_sample(self.root, 2)

    def test_unaccepted_requests_and_false_latency_fail(self):
        source = self.read('requests.json')
        for change in [{'status': 503}, {'event': 'missing'}, {'latency_ms': -1}, {'latency_ms': 20}]:
            with self.subTest(change=change):
                records = copy.deepcopy(source)
                records[0].update(change)
                self.write('requests.json', records)
                with self.assertRaises(ValueError):
                    audit_sample(self.root, 2)

    def test_weaker_policy_and_faster_claim_fail(self):
        config = self.read('config.json')
        config['settings']['acceptor']['recvs']['plugins']['http']['require_durable_ack'] = False
        self.write('config.json', config)
        with self.assertRaises(ValueError):
            audit_sample(self.root, 2)
        self.fixture()
        metrics = self.read('sample.json')
        metrics['delivered_per_second'] *= 2
        self.write('sample.json', metrics)
        with self.assertRaises(ValueError):
            audit_sample(self.root, 2)


if __name__ == '__main__':
    unittest.main()
