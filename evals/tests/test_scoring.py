import unittest
from yoloeval.provider import priced_cost, usage_of
from yoloeval.scoring import compare, score, summarize
from yoloeval.catalog import tasks


def row(task,trial,passed,seconds,tokens=100):
    return {'task_id':task,'trial':trial,'status':'completed','passed':passed,
            'timing':{'agent_wall_s':seconds,'verified_wall_s':seconds+2},
            'usage':{'usage_complete':True,'tokens_reported':{'total':tokens}},'cost_usd':None}


class ScoringTests(unittest.TestCase):
    def test_failure_cannot_be_bought_with_partial_credit(self):
        checks=[{'id':'a','group':'acceptance','critical':True,'passed':False},
                {'id':'b','group':'regression','critical':True,'passed':True}]
        self.assertFalse(score(checks)['passed'])
        self.assertFalse(score([])['passed'])
        self.assertFalse(score([{'id':'a','group':'acceptance','passed':True}],agent_error='timeout')['passed'])

    def test_failures_count_toward_efficiency(self):
        s=summarize([row('a',1,True,10,100),row('b',1,False,90,900)])
        self.assertEqual(s['agent_seconds_per_success_including_failures'],100)
        self.assertEqual(s['reported_tokens_per_success_including_failures'],1000)
        self.assertIsNone(s['cost_usd'])

    def test_regression_blocks_promotion_despite_speed(self):
        a=[row(f'a{i}',j,True,20) for i in range(5) for j in range(1,4)]
        b=[row(f'a{i}',j,True,5) for i in range(5) for j in range(1,4)]
        self.assertTrue(compare(a,b)['promotion_candidate'])
        b[0]['passed']=False
        self.assertFalse(compare(a,b)['promotion_candidate'])

    def test_pairing_and_infra_errors_fail_closed(self):
        a=[row('a',1,True,20)]
        with self.assertRaises(ValueError):compare(a,[])
        with self.assertRaises(ValueError):compare(a,a+a)
        b=[{**a[0],'status':'infra_error'}]
        with self.assertRaises(ValueError):compare(a,b)

    def test_unknown_usage_is_not_zero_cost(self):
        self.assertIsNone(usage_of({}))
        self.assertEqual(usage_of({'usage':{'prompt_tokens':100,'completion_tokens':4,'prompt_tokens_details':{'cached_tokens':20}}}),{'input':100,'output':4,'cached':20,'total':104,'consistent':True})
        odd=usage_of({'usage':{'input_tokens':10,'output_tokens':4,'input_tokens_details':{'cached_tokens':100}}})
        self.assertFalse(odd['consistent'])
        self.assertIsNone(priced_cost([{'usage':odd,'requested_model':'m'}],{'m':{'input_per_million':1,'output_per_million':1,'cached_input_per_million':1}}))
        self.assertIsNone(priced_cost([{'usage':None,'requested_model':'m'}],{}))
        prices={'m':{'input_per_million':1,'output_per_million':4,'cached_input_per_million':.5}}
        self.assertAlmostEqual(priced_cost([{'usage':{'input':100,'output':10,'cached':20},'requested_model':'m'}],prices),.00013)

    def test_catalog_balance(self):
        all_tasks=tasks();self.assertEqual(len(all_tasks),20)
        self.assertEqual(len(tasks('dev')),0);self.assertEqual(len(tasks('holdout')),20)
        for app in {t.app for t in all_tasks}:
            self.assertEqual({t.kind for t in all_tasks if t.app==app},{'create','edit','feature','bug'})


if __name__=='__main__':unittest.main()
