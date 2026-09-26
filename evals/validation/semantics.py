"""Additional model-free semantic controls, including the actual false negative."""
import argparse
import shutil
import sqlite3
from pathlib import Path
from unittest.mock import patch
from yoloeval.catalog import get_task
from yoloeval.fixtures import materialize
from yoloeval.runner import execute,write_json

def main():
    parser=argparse.ArgumentParser();parser.add_argument('--output',type=Path,required=True);parser.add_argument('--binary',type=Path,required=True);parser.add_argument('--original-app',type=Path,required=True);args=parser.parse_args()
    out=args.output.resolve();out.mkdir(parents=True)
    cases=[('search-notes-reference','notes.create',None),('search-csv-reference','csv.feature',None),('original-generated-notes','notes.create',None),('global-accent','notes.edit','ui.contract'),('existing-data-erasure','notes.feature','storage.existing_records')]
    rows=[]
    for name,task,expected_failure in cases:
        def altered(task,destination,control):
            protected=materialize(task,destination,control)
            if name.startswith('search-'):
                p=destination/'frontend/src/App.tsx';text=p.read_text();needle='<label>Search '+('notes' if task.app=='notes' else 'rows')+'<input '
                assert text.count(needle)==1;text=text.replace(needle,needle+'type="search" ');p.write_text(text)
            elif name=='original-generated-notes':
                # Fresh creation workspace + exact generated source, not an
                # actor rerun or a replacement for the invalid original score.
                for directory in ('frontend','backend'):
                    for p in (args.original_app/directory).rglob('*'):
                        if p.is_file() and p.suffix in ('.ts','.tsx','.css','.js','.html','.json'):
                            target=destination/p.relative_to(args.original_app);target.parent.mkdir(parents=True,exist_ok=True);shutil.copy2(p,target)
            elif name=='global-accent':
                p=destination/'frontend/src/style.css';p.write_text(p.read_text().replace('--accent: #2563eb','--accent: #7c3aed'))
            else:
                with sqlite3.connect(destination/'backend/data.db') as db:db.execute('DELETE FROM notes')
            return protected
        with patch('yoloeval.runner.materialize',side_effect=altered):
            row=execute(get_task(task),0,out/name,args.binary.resolve(),control='solution')
        failed=[c['id'] for c in row.get('checks',[]) if not c['passed']]
        valid=row['status']=='completed' and (row['passed'] if expected_failure is None else not row['passed'] and expected_failure in failed)
        record={'name':name,'valid':valid,'expected_failure':expected_failure,'failed_checks':failed};rows.append(record);print(record,flush=True)
    write_json(out/'validation.json',{'passed':all(r['valid'] for r in rows),'paid_model_calls':0,'controls':rows,'original_source':str(args.original_app.resolve()),'note':'The original generated app is rechecked only as a diagnostic control, never substituted for a paid result.'})
    return 0 if all(r['valid'] for r in rows) else 1
if __name__=='__main__':raise SystemExit(main())
