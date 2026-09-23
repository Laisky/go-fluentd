import unittest
from group_compare import PROFILES, read_service, summarize, selected_profiles

class EvidenceTests(unittest.TestCase):
    def fixture(self):
        return '\n'.join(f'BenchmarkJournalDurableService/{p}-2 2048 1000 ns/op 10 B/op 1 allocs/op 1 syncs/msg 1 msgs/op 1000000 msgs/s' for p in sorted(PROFILES))+'\nPASS\n'
    def test_complete_real_unit_shape(self):
        self.assertEqual(set(read_service(self.fixture(),2048)),PROFILES)
    def test_missing_duplicate_or_changed_units_rejected(self):
        text=self.fixture()
        for value in (text.replace('PASS','FAIL'),text+'\n'+text,text.replace('2048','1024'),text.replace('1 msgs/op','64 msgs/op'),text.replace('1 syncs/msg','0 syncs/msg'),text.replace('1000 ns/op','NaN ns/op'),'\n'.join(text.splitlines()[1:])):
            with self.subTest(value=value),self.assertRaises(ValueError):read_service(value,2048)
    def test_missing_pair_rejected(self):
        with self.assertRaises(ValueError):summarize([],'service',3)
    def test_report_preserves_slower_samples(self):
        rows=[]
        for p in PROFILES:
            for n in range(3):
                for v,ns in [('before',1000),('after',1200)]:
                    rows.append({'profile':p,'variant':v,'repeat':n,'metrics':{'ns/op':ns,'syncs/msg':1,'B/op':10,'allocs/op':1}})
        out=summarize(rows,'service',3)
        for value in out.values():self.assertAlmostEqual(value['ns/op']['median_change_percent'],20)
        with self.assertRaises(ValueError):summarize(rows+rows[:1],'service',3)

class ShardTests(unittest.TestCase):
    def rows(self):
        return [{'profile': 'gzip=false/clients=16', 'variant': v, 'repeat': n,
                 'metrics': {'ns/op': x, 'syncs/msg': 1, 'B/op': 10, 'allocs/op': 1}}
                for n in range(6) for v,x in [('before',1000),('after',1200)]]

    def test_exact_shard_retains_six_pairs_and_slower_results(self):
        profile = ['gzip=false/clients=16']
        result = summarize(self.rows(), 'service', 6, profile)
        self.assertEqual(set(result), set(profile))
        self.assertEqual(len(result[profile[0]]['ns/op']['after']['samples']), 6)
        self.assertAlmostEqual(result[profile[0]]['ns/op']['median_change_percent'], 20)

    def test_shards_cannot_weaken_completeness(self):
        rows = self.rows()
        # Without explicit scoping, all workloads must still be present.
        with self.assertRaises(ValueError):
            summarize(rows, 'service', 6)
        for bad in (rows[:-1], rows+rows[:1], rows+[dict(rows[0], variant='ignored')],
                    rows+[dict(rows[0], profile='gzip=true/clients=16')]):
            with self.subTest(rows=bad), self.assertRaises(ValueError):
                summarize(bad, 'service', 6, ['gzip=false/clients=16'])

    def test_unknown_empty_and_duplicate_shards_are_rejected(self):
        self.assertEqual(selected_profiles(), PROFILES)
        for bad in ([], ['invalid'], ['gzip=false/clients=1']*2):
            with self.subTest(profiles=bad), self.assertRaises(ValueError):
                selected_profiles(bad)

if __name__=='__main__':unittest.main()
