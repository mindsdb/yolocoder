"""Frozen serial validation against original vanilla; no candidate tuning."""
import argparse
import hashlib
import json
import random
import statistics
from datetime import datetime,timezone
from pathlib import Path

from .catalog import tasks
from .fixtures import suite_digest,dependency_digest
from .provider import load_provider
from .report import report
from .runner import execute,manifest,write_json
from .scoring import quantile,compare

CRITICAL={'storage.existing_records','storage.backend_restart'}
N=60

def plan(seed=20260926):
    rng=random.Random(seed);pairs=[]
    # Difficult tasks first, randomized within fixed difficulty blocks.
    for kind in ('create','feature','bug','edit'):
        block=[(t,i) for t in tasks() if t.kind==kind for i in (1,2,3)]
        rng.shuffle(block);pairs.extend(block)
    first=rng.randrange(2)
    return [(t,i,('baseline','candidate') if (j+first)%2==0 else ('candidate','baseline')) for j,(t,i) in enumerate(pairs)]

def decision(rows,n=N):
    a,b=rows['baseline'],rows['candidate']
    if any(r['status']=='infra_error' for r in a+b):return {'status':'blocked_infrastructure','complete':False,'accepted':False,'reason':'Infrastructure error; original result preserved, no retry.'}
    keyed={role:{(r['task_id'],r['trial']):r for r in rr} for role,rr in rows.items()}
    regressions=[]
    for key in keyed['baseline'].keys() & keyed['candidate'].keys():
        ac={c['id']:c['passed'] for c in keyed['baseline'][key].get('checks',[])}
        bc={c['id']:c['passed'] for c in keyed['candidate'][key].get('checks',[])}
        for c in CRITICAL:
            if ac.get(c) is True and bc.get(c) is False:regressions.append({'task_id':key[0],'trial':key[1],'check':c})
    counts={k:{'completed':len(v),'passed':sum(r['passed'] for r in v)} for k,v in rows.items()}
    full=len(a)==len(b)==n
    base={'complete':full,'accepted':False,'counts':counts,'critical_regressions':regressions}
    if regressions:return {**base,'status':'rejected' if full else 'stopped_early','reason':'Paired storage-preservation regression violates the fixed gate.'}
    if sum(r['passed'] for r in b)+n-len(b)<sum(r['passed'] for r in a):return {**base,'status':'rejected' if full else 'stopped_early','reason':'Even all remaining candidate successes cannot match observed vanilla passes.'}
    times={k:[r['timing']['agent_wall_s'] for r in rr] for k,rr in rows.items()}
    # Unknown vanilla durations have no assumed upper bound. Therefore only
    # stop on speed once all vanilla timings are known. This is conservative
    # but remains valid when timeout cleanup adds time beyond the actor budget.
    if len(a)==n:
        optimistic=times['candidate']+[0.]*(n-len(b))
        lower_mean=sum(optimistic)/n;lower_p50=quantile(optimistic,.5)
        baseline_mean=statistics.mean(times['baseline']);baseline_p50=quantile(times['baseline'],.5)
        metrics={'baseline_mean_s':baseline_mean,'baseline_p50_s':baseline_p50,
                 'candidate_mean_lower_bound_s':lower_mean,'candidate_p50_lower_bound_s':lower_p50}
        base['timing_bounds']=metrics
        if lower_mean>=baseline_mean or lower_p50>=baseline_p50:return {**base,'status':'rejected' if full else 'stopped_early','reason':'Candidate cannot improve both mean and median, even with zero-time remaining attempts.'}
    if full:
        return {**base,'status':'accepted','accepted':True,'reason':'Equal or higher pass count, no paired storage regressions, lower mean and median.',
                'perfect_candidate':all(r['passed'] for r in b),
                'candidate_mean_s':statistics.mean(times['candidate']),'candidate_p50_s':quantile(times['candidate'],.5)}
    return {**base,'status':'running','reason':'Remaining outcomes could still satisfy all gates.'}

def main():
    parser=argparse.ArgumentParser();parser.add_argument('--output',type=Path,required=True);parser.add_argument('--calibration',type=Path,required=True);parser.add_argument('--binaries',type=Path,required=True);parser.add_argument('--actors',type=Path,required=True,help='Build receipt from reproduce.py prepare; hashes and source labels frozen before evaluation')
    args=parser.parse_args();out=args.output.resolve();out.mkdir(parents=True,exist_ok=True)
    calibration=json.loads(args.calibration.read_text())
    if not calibration['passed'] or len(calibration['controls'])!=40:raise ValueError('Need all 40 valid offline controls')
    receipt_path=args.calibration.parent/'freeze.json';receipt=json.loads(receipt_path.read_text())
    suite,dependencies=suite_digest(),dependency_digest()
    if receipt['suite_digest']!=suite or receipt['dependency_digest']!=dependencies:raise ValueError('Suite/dependencies changed since final calibration freeze')
    saved=load_provider()
    if saved['base_url'].rstrip('/')!='https://api.mindshub.ai':raise ValueError('Unexpected provider')
    actors=json.loads(args.actors.read_text())
    hashes={role:actors[role]['binary_sha256'] for role in ('baseline','candidate')}
    if receipt['binary_sha256']!=hashes:raise ValueError('Actor binaries differ from offline calibration')
    schedule=plan();public_schedule=[{'task_id':t.id,'trial':i,'order':order} for t,i,order in schedule]
    roles={};rows={'baseline':[],'candidate':[]}
    for role,file in [('baseline','vanilla'),('candidate','winner')]:
        binary=(args.binaries/file).resolve()
        if hashlib.sha256(binary.read_bytes()).hexdigest()!=hashes[role]:raise ValueError('Frozen binary mismatch: '+role)
        provider={**saved,'model':'muse-spark-1-3','api':'responses','allow_models':['jev-1.13.0'] if role=='candidate' else []}
        folder=out/role;folder.mkdir(exist_ok=True)
        config=manifest(binary,provider,tasks(),3,20260926,role)
        config.update({'timeout_s':180,'max_model_calls_per_attempt':30,'prices':{},'pairing':'interleaved-ab-ba-difficulty-blocks','origin':actors[role]['revision']})
        path=folder/'manifest.json'
        if path.exists() and json.loads(path.read_text())!=config:raise ValueError('Resume manifest mismatch')
        if not path.exists():write_json(path,config)
        roles[role]={'binary':binary,'provider':provider,'folder':folder}
    protocol={'suite_digest':suite,'dependency_digest':dependencies,'binary_sha256':hashes,'actors':actors,'calibration_sha256':hashlib.sha256(args.calibration.read_bytes()).hexdigest(),
       'planned_attempts':120,'coding_model':'muse-spark-1-3','router_model_candidate_only':'jev-1.13.0','timeout_s':180,'max_forwarded_calls':30,
       'quality_rule':'candidate passes >= original vanilla passes; all required checks must pass per attempt',
       'critical_rule':'no paired candidate failure of storage.existing_records or storage.backend_restart when vanilla passes that check',
       'speed_rule':'strictly lower arithmetic mean AND nearest-rank P50 of all attempts including failures; raw agent_wall_s, setup/grading excluded',
       'early_stop_rule':'after drained attempt: irrecoverable pass-count deficit; paired critical regression; or optimistic speed bound after all vanilla rows known. No assumed upper bound for unobserved vanilla times.',
       'failure_policy':'Preserve every attempt. Stop on infrastructure gaps. No scored retries, no actor/harness changes after first paid call. Early stopped runs are incomplete.',
       'limitations':'Five app clusters, three repeats per task; repeated attempts are not independent applications. Functional/accessibility-name/mobile checks do not establish comprehensive security or visual design quality. Historical exposed suite is excluded.',
       'schedule':public_schedule}
    path=out/'preregistration.json'
    if path.exists():
        old=json.loads(path.read_text());old.pop('registered_at',None)
        if old!=protocol:raise ValueError('Preregistered protocol changed')
    else:write_json(path,{**protocol,'registered_at':datetime.now(timezone.utc).isoformat()})
    for index,(task,trial,order) in enumerate(schedule,1):
        for role in order:
            state=roles[role];folder=state['folder']/f'{task.id}--{trial}';result=folder/'result.json'
            if result.exists():row=json.loads(result.read_text())
            elif folder.exists():raise ValueError(f'Interrupted attempt preserved at {folder}; cannot retry')
            else:
                print(f'[{index}/60] {role} {task.id} trial {trial}',flush=True)
                row=execute(task,trial,folder,state['binary'],state['provider'],180)
                print(f"  {'PASS' if row['passed'] else 'FAIL'} {row.get('timing',{}).get('agent_wall_s',0):.2f}s",flush=True)
            rows[role].append(row)
            if suite_digest()!=suite or dependency_digest()!=dependencies or any(hashlib.sha256(v['binary'].read_bytes()).hexdigest()!=hashes[k] for k,v in roles.items()):
                write_json(out/'decision.json',{'status':'invalidated','accepted':False,'reason':'Frozen evaluator/dependencies/binary changed'});raise RuntimeError('Integrity mismatch')
            report(state['folder'],rows[role])
            d=decision(rows);write_json(out/'decision.json',d)
            if d['status']!='running':
                if d['complete']:
                    comparison=compare(rows['baseline'],rows['candidate']);comparison.pop('promotion_candidate',None);comparison.pop('promotion_note',None);write_json(out/'descriptive-comparison.json',comparison)
                print(json.dumps(d,indent=2),flush=True);return

if __name__=='__main__':main()
