import contextlib
import difflib
import hashlib
import json
import os
import platform
import random
import shutil
import subprocess
import time
import traceback
from datetime import datetime, timezone
from pathlib import Path

from .catalog import get_task, tasks
from .fixtures import ROOT, PROTECTED, hashes, materialize, suite_digest, dependency_digest
from .provider import Recorder, load_provider, priced_cost
from .runtime import Runtime
from .scoring import score


def write_json(path, value):
    temporary=path.with_suffix(path.suffix+'.tmp')
    temporary.write_text(json.dumps(value,indent=2));temporary.replace(path)


def sources(workspace):
    paths=[]
    for directory in ('frontend','backend'):
        paths.extend(p for p in (workspace/directory).rglob('*') if p.is_file() and p.suffix in ('.ts','.tsx','.css','.html','.js','.json'))
    return {str(p.relative_to(workspace)):p.read_text(errors='replace') for p in paths}


def diff_sources(before, after):
    return '\n'.join(''.join(difflib.unified_diff(before.get(p,'').splitlines(True),after.get(p,'').splitlines(True),fromfile='a/'+p,tofile='b/'+p)) for p in sorted(before.keys()|after.keys()) if before.get(p)!=after.get(p))


def grade(task,runtime,output,protected):
    start=time.monotonic();checks=[]
    altered=[p for p in PROTECTED if hashes(runtime.workspace)[p]!=protected[p]]
    checks.append({'id':'integrity.public_runtime','group':'regression','critical':True,'passed':not altered,'error':', '.join(altered) if altered else None})
    try:
        result=subprocess.run(['node',str(ROOT/'.cache/runtime/node_modules/typescript/bin/tsc'),'--noEmit','-p',str(runtime.workspace/'tsconfig.json')],cwd=runtime.workspace,env=runtime.env,capture_output=True,text=True,timeout=45)
        (output/'typecheck.log').write_text(result.stdout+result.stderr)
        checks.append({'id':'build.typecheck','group':'regression','critical':True,'passed':result.returncode==0})
    except subprocess.TimeoutExpired:
        checks.append({'id':'build.typecheck','group':'regression','critical':True,'passed':False,'error':'45s timeout'})
    try:
        result=subprocess.run(['node',str(ROOT/'browser/grade.mjs'),task.app,task.kind,runtime.url,runtime.control,str(output/'evidence'),task.id.replace('.','-')],cwd=ROOT,capture_output=True,text=True,timeout=150)
        (output/'browser.log').write_text(result.stdout+result.stderr)
        path=output/'evidence/browser.json'
        if path.exists():checks.extend(json.loads(path.read_text())['checks'])
        else:checks.append({'id':'browser.completed','group':'acceptance','critical':True,'passed':False,'error':'Browser checker did not finish; see browser.log'})
    except subprocess.TimeoutExpired:
        checks.append({'id':'browser.completed','group':'acceptance','critical':True,'passed':False,'error':'150s timeout'})
    return checks,time.monotonic()-start


def execute(task,trial,folder,binary,provider=None,timeout=180,control='start',prices=None):
    folder.mkdir(parents=True)
    workspace=folder/'app'
    protected=materialize(task,workspace,control)
    subprocess.run(['git','init','-q'],cwd=workspace,check=True,capture_output=True)
    subprocess.run(['git','add','.'],cwd=workspace,check=True,capture_output=True)
    before=sources(workspace)
    (folder/'prompt.txt').write_text(task.prompt)
    result={'schema_version':1,'task_id':task.id,'app':task.app,'kind':task.kind,'split':task.split,'trial':trial,'control':control,'status':'completed','started_at':datetime.now(timezone.utc).isoformat()}
    recorder=Recorder(provider,folder/'calls.json') if provider else None
    context=recorder if recorder else contextlib.nullcontext()
    boot=time.monotonic()
    phase='setup'
    try:
        with context:
            with Runtime(workspace,folder,binary,recorder.url if recorder else 'http://127.0.0.1:9/v1',provider['model'] if provider else 'offline-control',provider.get('api','responses') if provider else 'responses') as runtime:
                runtime.ready()
                result['setup_wall_s']=time.monotonic()-boot
                phase='turn'
                task_start=time.monotonic()
                outcome=runtime.task(task.prompt,timeout) if provider else {'agent_wall_s':0,'agent_error':None,'reply':None,'events':[],'agent_reply_s':None}
                result['agent_error']=outcome['agent_error']
                result['reply']=outcome['reply']
                phase='grading'
                if runtime.proc.poll() is None:
                    checks,grader_s=grade(task,runtime,folder,protected)
                else:
                    grader_s=0;checks=[{'id':'runtime.alive','group':'acceptance','critical':True,'passed':False,'error':'Agent timed out or exited'}]
                result['checks']=checks
                result.update(score(checks,result['agent_error']))
                result['timing']={'agent_wall_s':outcome['agent_wall_s'],'agent_reply_s':outcome['agent_reply_s'],
                    'grader_wall_s':grader_s,'verified_wall_s':time.monotonic()-task_start,
                    'time_to_correct_visible_change_s':None}
                (folder/'changes.diff').write_text(diff_sources(before,sources(workspace)))
                phase='cleanup'
    except Exception as error:
        detail=traceback.format_exc()
        if provider:detail=detail.replace(provider['api_key'],'[REDACTED]')
        (folder/'harness-error.log').write_text(detail)
        result.update({'status':'infra_error','passed':False,'quality_score':0,'infra_stage':phase,'infra_error':f'{type(error).__name__}: {error}'})
    finally:
        result['usage']=recorder.summary() if recorder else {'model_calls':0,'failed_calls':0,'usage_complete':True,'tokens_reported':dict.fromkeys(('input','output','cached','total'),0),'model_wait_s':0}
        result['cost_usd']=priced_cost(recorder.calls,prices or {}) if recorder else 0
        write_json(folder/'result.json',result)
    return result


def selected_tasks(split='all',ids=None):
    selected=tasks(split)
    if ids:
        wanted=set(ids.split(','));available={t.id for t in tasks()}
        if not wanted<=available:raise ValueError(f'Unknown tasks: {sorted(wanted-available)}')
        selected=[t for t in selected if t.id in wanted]
    if not selected:raise ValueError('No tasks selected')
    return selected


def manifest(binary,provider,selected,repeats,seed,candidate):
    version=subprocess.run([str(binary),'--version'],env={**os.environ,'YOLOCODER_NO_AUTOUPDATE':'1'},capture_output=True,text=True,check=True).stdout.strip()
    return {'schema_version':1,'suite_digest':suite_digest(),'dependency_digest':dependency_digest(),'candidate':candidate,'binary_sha256':hashlib.sha256(binary.read_bytes()).hexdigest(),
        'binary_version':version,'model':provider['model'],'allow_models':provider.get('allow_models',[]),'provider':provider['base_url'],'api':provider.get('api','responses'),
        'tasks':[t.id for t in selected],'repeats':repeats,'order_seed':seed,'mode':'web-warm','machine':{'system':platform.system(),'machine':platform.machine(),'release':platform.release(),'node':subprocess.check_output(['node','--version'],text=True).strip()},
        'measurement_note':'Setup excluded; agent_wall_s ends when the web turn ends. verified_wall_s includes independent grading. First-correct-preview latency is not measured.'}


def run(args):
    provider=load_provider()
    if args.model:provider['model']=args.model
    provider['allow_models']=sorted(set(args.allow_model or []))
    selected=selected_tasks(args.split,args.tasks)
    out=args.output.resolve();out.mkdir(parents=True,exist_ok=True)
    config=manifest(args.binary,provider,selected,args.repeats,args.seed,args.candidate)
    prices=json.loads(args.prices.read_text()) if args.prices else {}
    config.update({'timeout_s':args.timeout,'prices':prices,'max_model_calls_per_attempt':30})
    path=out/'manifest.json'
    if path.exists():
        if json.loads(path.read_text())!=config:raise ValueError('Resume configuration differs from the saved manifest')
    else:write_json(path,config)
    schedule=[(task,trial) for trial in range(1,args.repeats+1) for task in selected]
    random.Random(args.seed).shuffle(schedule)
    rows=[]
    for index,(task,trial) in enumerate(schedule,1):
        folder=out/f'{task.id}--{trial}'
        if (folder/'result.json').exists():row=json.loads((folder/'result.json').read_text())
        elif folder.exists():raise ValueError(f'Interrupted attempt at {folder}; preserve it and use a fresh run directory rather than silently retrying')
        else:
            print(f'[{index}/{len(schedule)}] {task.id} trial {trial}',flush=True)
            row=execute(task,trial,folder,args.binary,provider,args.timeout,prices=prices)
            print(f"  {'PASS' if row['passed'] else row['status'].upper() if row['status']=='infra_error' else 'FAIL'} · {row.get('timing',{}).get('agent_wall_s',0):.1f}s · {row['usage']['tokens_reported']['total']} reported tokens",flush=True)
        if suite_digest()!=config['suite_digest'] or dependency_digest()!=config['dependency_digest'] or hashlib.sha256(args.binary.read_bytes()).hexdigest()!=config['binary_sha256']:
            write_json(out/'invalidated.json',{'reason':'Evaluator, fixtures, dependencies or actor binary changed during the campaign','after_task':task.id,'trial':trial})
            raise RuntimeError('Campaign invalidated: suite or dependencies changed')
        rows.append(row)
        from .report import report
        report(out,rows)
    return rows


def validate(args):
    out=args.output.resolve();out.mkdir(parents=True,exist_ok=True)
    selected=selected_tasks('all',args.tasks)
    rows=[]
    for task in selected:
        for control in ('solution','start'):
            folder=out/f'{task.id}--{control}'
            if (folder/'result.json').exists():row=json.loads((folder/'result.json').read_text())
            else:row=execute(task,0,folder,args.binary,control=control)
            expected=control=='solution'
            failure_ids={c['id'] for c in row.get('checks',[]) if not c['passed']}
            bug_checks={app:'bug.'+app for app in ('csv','quiz','inventory','notes','application')}
            feature_checks={app:'feature.'+app for app in bug_checks}
            intended=bug_checks[task.app] if task.kind=='bug' else feature_checks[task.app] if task.kind=='feature' else 'ui.contract'
            valid=row['status']!='infra_error' and row['passed']==expected
            if not expected:
                valid &= intended in failure_ids and 'build.typecheck' not in failure_ids and 'integrity.public_runtime' not in failure_ids
                if task.kind!='create': valid &= failure_ids=={intended}
            rows.append({'task_id':task.id,'control':control,'expected_pass':expected,'passed':row['passed'],'valid':valid,'failed_checks':sorted(failure_ids),'status':row['status']})
            print(f"{'OK' if valid else 'BROKEN'} {task.id} {control}: expected {expected}, got {row['passed']}",flush=True)
    write_json(out/'validation.json',{'passed':all(r['valid'] for r in rows),'controls':rows})
    return all(r['valid'] for r in rows)
