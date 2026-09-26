import unittest
import contextlib
import hashlib
import io
import json
import tempfile
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch
from yoloeval.catalog import tasks
from yoloeval.paired import schedule,run_paired


class PairingTests(unittest.TestCase):
    def test_every_pair_once_and_order_balanced(self):
        plan=schedule(tasks('holdout'),3,42)
        self.assertEqual(len(plan),60)
        self.assertEqual(len({(task.id,trial) for task,trial,_ in plan}),60)
        firsts=[order[0] for _,_,order in plan]
        self.assertLessEqual(abs(firsts.count('baseline')-firsts.count('candidate')),1)
        self.assertEqual(plan,schedule(tasks('holdout'),3,42))
        self.assertNotEqual(plan,schedule(tasks('holdout'),3,43))
        for _,_,order in plan:self.assertEqual(set(order),{'baseline','candidate'})

    def test_complete_run_resumes_without_repeating_attempts(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);binary=root/'binary';binary.write_bytes(b'test binary')
            fingerprint=hashlib.sha256(binary.read_bytes()).hexdigest()
            args=SimpleNamespace(split='holdout',tasks='notes.edit,inventory.edit',repeats=3,seed=42,timeout=180,prices=None,output=root/'run',
                baseline_binary=binary,candidate_binary=binary,baseline_model=None,candidate_model=None,baseline_allow_model=None,candidate_allow_model=['jev-test'])
            calls=[]
            def execute(task,trial,folder,*args,**kwargs):
                folder.mkdir(parents=True);role=folder.parent.name;calls.append((task.id,trial,role))
                row={'task_id':task.id,'trial':trial,'kind':task.kind,'status':'completed','passed':True,
                     'timing':{'agent_wall_s':10 if role=='baseline' else 5,'verified_wall_s':12},
                     'usage':{'usage_complete':True,'tokens_reported':{'total':100}},'cost_usd':None}
                (folder/'result.json').write_text(json.dumps(row));return row
            config={'suite_digest':'suite','dependency_digest':'deps','binary_sha256':fingerprint}
            with patch('yoloeval.paired.load_provider',return_value={'model':'test','base_url':'https://example.invalid','api_key':'test'}), \
                 patch('yoloeval.paired.manifest',side_effect=lambda *a:dict(config)), \
                 patch('yoloeval.paired.suite_digest',return_value='suite'), \
                 patch('yoloeval.paired.dependency_digest',return_value='deps'), \
                 patch('yoloeval.paired.execute',side_effect=execute),contextlib.redirect_stdout(io.StringIO()):
                first=run_paired(args);second=run_paired(args)
            self.assertEqual(len(calls),12);self.assertEqual(first,second)
            self.assertEqual(first['joint_success_pairs'],6);self.assertEqual(first['speedup_geomean'],2)
            self.assertFalse(first['promotion_candidate'])  # only two task families
            self.assertEqual(len(json.loads((args.output/'schedule.json').read_text())),6)


if __name__=='__main__':unittest.main()
