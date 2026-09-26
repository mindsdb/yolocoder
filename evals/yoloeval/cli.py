import argparse
import json
import shutil
import subprocess
from pathlib import Path

from .catalog import tasks
from .fixtures import ROOT
from .report import load_results, report
from .runner import run, validate, write_json
from .scoring import compare


def binary_path(value):
    path=Path(value).expanduser()
    if not path.is_file():
        found=shutil.which(value)
        if not found:raise argparse.ArgumentTypeError('YoloCoder binary not found')
        path=Path(found)
    return path.resolve()


def main():
    parser=argparse.ArgumentParser(description='YoloCoder black-box app evaluation suite')
    subs=parser.add_subparsers(dest='command',required=True)
    subs.add_parser('list')
    subs.add_parser('setup')
    for name in ('run','validate'):
        p=subs.add_parser(name)
        p.add_argument('--binary',type=binary_path,default=Path.home()/'.local/bin/yolocoder')
        p.add_argument('--output',type=Path,required=True)
        p.add_argument('--tasks',help='Comma-separated task IDs')
        if name=='run':
            p.add_argument('--split',choices=['all','dev','holdout'],default='dev')
            p.add_argument('--repeats',type=int,default=3)
            p.add_argument('--seed',type=int,default=20260922)
            p.add_argument('--timeout',type=int,default=180)
            p.add_argument('--candidate',default='baseline')
            p.add_argument('--model')
            p.add_argument('--allow-model',action='append',help='Additional exact model ID permitted through the recorder, e.g. jev-1.13.0; repeat for routed candidates')
            p.add_argument('--prices',type=Path)
    p=subs.add_parser('duel',help='Interleave a baseline and candidate on matching task/repetition pairs')
    p.add_argument('--output',type=Path,required=True);p.add_argument('--tasks')
    p.add_argument('--split',choices=['all','dev','holdout'],default='dev')
    p.add_argument('--repeats',type=int,default=3);p.add_argument('--seed',type=int,default=20260922)
    p.add_argument('--timeout',type=int,default=180);p.add_argument('--prices',type=Path)
    for role in ('baseline','candidate'):
        p.add_argument('--'+role+'-binary',type=binary_path,default=Path.home()/'.local/bin/yolocoder')
        p.add_argument('--'+role+'-model')
        p.add_argument('--'+role+'-allow-model',action='append')
    p=subs.add_parser('report');p.add_argument('folder',type=Path)
    p=subs.add_parser('compare');p.add_argument('baseline',type=Path);p.add_argument('candidate',type=Path);p.add_argument('--output',type=Path,required=True)
    p=subs.add_parser('judge');p.add_argument('folder',type=Path);p.add_argument('--provider',choices=['mindshub','typesafe'],default='mindshub');p.add_argument('--model');p.add_argument('--limit',type=int,default=5)
    args=parser.parse_args()
    if args.command=='list':
        print(json.dumps([t.to_dict() for t in tasks()],indent=2));return 0
    if args.command=='setup':
        subprocess.run(['npm','ci','--ignore-scripts'],cwd=ROOT,check=True)
        runtime=ROOT/'.cache/runtime';runtime.mkdir(parents=True,exist_ok=True)
        for name in ('package.json','package-lock.json'):shutil.copy2(ROOT/'fixtures/common'/name,runtime/name)
        subprocess.run(['npm','ci','--no-audit','--no-fund'],cwd=runtime,check=True)
        subprocess.run(['npx','playwright','install','chromium'],cwd=ROOT,check=True);return 0
    if args.command=='validate':return 0 if validate(args) else 1
    if args.command=='run':
        if args.repeats<1 or args.timeout<1:parser.error('repeats and timeout must be positive')
        run(args);return 0
    if args.command=='duel':
        if args.repeats<1 or args.timeout<1:parser.error('repeats and timeout must be positive')
        from .paired import run_paired
        run_paired(args);return 0
    if args.command=='report':print(json.dumps(report(args.folder),indent=2));return 0
    if args.command=='compare':
        if any((p/'invalidated.json').exists() for p in (args.baseline,args.candidate)):raise ValueError('Cannot compare an invalidated campaign')
        a=json.loads((args.baseline/'manifest.json').read_text());b=json.loads((args.candidate/'manifest.json').read_text())
        for key in ('suite_digest','dependency_digest','mode','tasks','repeats','order_seed','machine','timeout_s','max_model_calls_per_attempt'):
            if a[key]!=b[key]:raise ValueError(f'Incomparable runs: {key} differs')
        if a.get('pairing','separate')!=b.get('pairing','separate'):raise ValueError('Incomparable run ordering protocols')
        for folder,config in ((args.baseline,a),(args.candidate,b)):
            expected={(task,trial) for task in config['tasks'] for trial in range(1,config['repeats']+1)}
            actual={(r['task_id'],r['trial']) for r in load_results(folder)}
            if actual!=expected:raise ValueError(f'Incomplete campaign: {folder}')
        result=compare(load_results(args.baseline),load_results(args.candidate));write_json(args.output,result);print(json.dumps(result,indent=2));return 0
    if args.command=='judge':
        from .judge import judge_run
        judge_run(args);return 0


if __name__=='__main__':
    raise SystemExit(main())
