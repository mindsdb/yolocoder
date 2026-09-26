"""Matched A/B runs with balanced, alternating order inside each task pair."""
import hashlib
import json
import random

from .fixtures import suite_digest,dependency_digest
from .provider import load_provider
from .report import report
from .runner import execute,manifest,selected_tasks,write_json
from .scoring import compare


def schedule(selected,repeats,seed):
    pairs=[(task,trial) for trial in range(1,repeats+1) for task in selected]
    rng=random.Random(seed);rng.shuffle(pairs);first=rng.randrange(2)
    return [(task,trial,('baseline','candidate') if (i+first)%2==0 else ('candidate','baseline')) for i,(task,trial) in enumerate(pairs)]


def run_paired(args):
    selected=selected_tasks(args.split,args.tasks)
    saved=load_provider();out=args.output.resolve();out.mkdir(parents=True,exist_ok=True)
    prices=json.loads(args.prices.read_text()) if args.prices else {}
    roles={}
    for role in ('baseline','candidate'):
        provider={**saved,'model':getattr(args,role+'_model') or saved['model'],
                  'allow_models':sorted(set(getattr(args,role+'_allow_model') or []))}
        binary=getattr(args,role+'_binary')
        folder=out/role;folder.mkdir(exist_ok=True)
        config=manifest(binary,provider,selected,args.repeats,args.seed,role)
        config.update({'timeout_s':args.timeout,'prices':prices,'max_model_calls_per_attempt':30,'pairing':'interleaved-ab-ba'})
        path=folder/'manifest.json'
        if path.exists() and json.loads(path.read_text())!=config:raise ValueError(f'Resume configuration changed: {role}')
        if not path.exists():write_json(path,config)
        roles[role]={'provider':provider,'binary':binary,'folder':folder,'config':config,'rows':[]}
    plan=schedule(selected,args.repeats,args.seed)
    write_json(out/'schedule.json',[{'task_id':t.id,'trial':trial,'order':order} for t,trial,order in plan])
    for index,(task,trial,order) in enumerate(plan,1):
        for role in order:
            state=roles[role];folder=state['folder']/f'{task.id}--{trial}'
            if (folder/'result.json').exists():row=json.loads((folder/'result.json').read_text())
            elif folder.exists():raise ValueError(f'Interrupted attempt at {folder}; preserve it and start a fresh campaign')
            else:
                print(f'[{index}/{len(plan)}] {role}: {task.id} trial {trial}',flush=True)
                row=execute(task,trial,folder,state['binary'],state['provider'],args.timeout,prices=prices)
                print(f"  {'PASS' if row['passed'] else row['status'].upper() if row['status']=='infra_error' else 'FAIL'}",flush=True)
            config=state['config']
            if suite_digest()!=config['suite_digest'] or dependency_digest()!=config['dependency_digest'] or hashlib.sha256(state['binary'].read_bytes()).hexdigest()!=config['binary_sha256']:
                for item in roles.values():write_json(item['folder']/'invalidated.json',{'reason':'Suite, dependencies or an actor binary changed'})
                raise RuntimeError('Paired campaign invalidated')
            state['rows'].append(row);report(state['folder'],state['rows'])
    try:comparison=compare(roles['baseline']['rows'],roles['candidate']['rows'])
    except ValueError as error:comparison={'status':'comparison_blocked','reason':str(error),'promotion_candidate':False}
    write_json(out/'comparison.json',comparison)
    return comparison
