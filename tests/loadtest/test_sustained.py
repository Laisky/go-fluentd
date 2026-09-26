import copy
import unittest
from sustained import assess
class SteadyGate(unittest.TestCase):
    def fixture(self):
        points=[{'at_ns':i*10**9,'cpu_seconds':i*2.4} for i in range(41)]
        records=[{'Start':i*10**7+1,'Sink':[i*10**7+1000000], 'Status':204,'Dropped':False} for i in range(4000)]
        return points,records
    def test_accepts_real_app_cpu_and_stable_completions(self):
        p,r=self.fixture();got=assess(p,r,0,0,40,4,concurrency=64)
        self.assertTrue(got['qualified']);self.assertAlmostEqual(got['mean_app_cpu_fraction'],.6)
    def test_rejects_idle_driver_only_and_spiky_load(self):
        p,r=self.fixture()
        for scale in (0,.2):
            bad=copy.deepcopy(p)
            for x in bad:x['cpu_seconds']*=scale
            self.assertFalse(assess(bad,r,0,0,40,4)['qualified'])
        self.assertFalse(assess(p,r[:2000],0,0,40,4)['qualified'])
        self.assertFalse(assess(p,r,0,0,10,4)['qualified'])
    def test_rejects_missing_or_dropped_records(self):
        for field,value in [('Sink',[0]),('Dropped',True)]:
            p,r=self.fixture();r[0][field]=value
            with self.assertRaises(AssertionError):assess(p,r,0,0,40,4)
    def test_rejects_backlog_outside_declared_window(self):
        p,r=self.fixture()
        for x in r:x['Sink']=[x['Start']+10**9]
        self.assertFalse(assess(p,r,0,0,40,4,concurrency=64)['qualified'])
if __name__=='__main__':unittest.main()
