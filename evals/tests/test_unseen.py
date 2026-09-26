import unittest
from yoloeval.unseen import decision,plan

def row(i,passed=True,time=10,critical=None,status='completed'):
    return {'task_id':str(i),'trial':1,'passed':passed,'status':status,'timing':{'agent_wall_s':time},'checks':[] if critical is None else [{'id':'storage.existing_records','passed':critical}]}

class Gates(unittest.TestCase):
    def test_recoverable_failure_does_not_stop(self):
        self.assertEqual(decision({'baseline':[row(1)],'candidate':[row(1,False)]},3)['status'],'running')
    def test_irrecoverable_quality_stops(self):
        self.assertEqual(decision({'baseline':[row(1),row(2)],'candidate':[row(1,False),row(2,False)]},3)['status'],'stopped_early')
    def test_no_invented_bound_on_future_vanilla(self):
        self.assertEqual(decision({'baseline':[row(1)],'candidate':[row(1,time=1000)]},3)['status'],'running')
    def test_mean_impossible_with_one_remaining(self):
        d=decision({'baseline':[row(i) for i in range(3)],'candidate':[row(0,time=20),row(1,time=20)]},3)
        self.assertEqual(d['status'],'stopped_early')
    def test_median_impossible_even_if_mean_could_win(self):
        d=decision({'baseline':[row(0,time=10),row(1,time=10),row(2,time=100)],'candidate':[row(0,time=11),row(1,time=11)]},3)
        self.assertEqual(d['status'],'stopped_early')
    def test_storage_requires_paired_control_check(self):
        rows={'baseline':[row(1,critical=None)],'candidate':[row(1,False,critical=False)]}
        self.assertEqual(decision(rows,3)['status'],'running')
        rows['baseline'][0]['checks']=[{'id':'storage.existing_records','passed':True}]
        self.assertEqual(decision(rows,3)['status'],'stopped_early')
    def test_equal_quality_lower_mean_and_median_accepted(self):
        d=decision({'baseline':[row(i,time=20,passed=i!=0) for i in range(3)],'candidate':[row(i,time=10,passed=i!=0) for i in range(3)]},3)
        self.assertTrue(d['accepted']);self.assertFalse(d['perfect_candidate'])
    def test_infra_is_never_acceptance(self):
        self.assertEqual(decision({'baseline':[row(1,status='infra_error')],'candidate':[]},3)['status'],'blocked_infrastructure')
    def test_balanced_complete_schedule(self):
        p=plan();self.assertEqual(len(p),60);self.assertEqual(len({(t.id,i) for t,i,_ in p}),60)
        self.assertEqual(sum(o[0]=='baseline' for _,_,o in p),30);self.assertTrue(all(t.kind=='create' for t,_,_ in p[:15]))

if __name__=='__main__':unittest.main()
