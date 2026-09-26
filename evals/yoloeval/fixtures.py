"""Materialize immutable starting points and positive/negative controls."""
import hashlib
import json
import shutil
import sqlite3
from pathlib import Path

from .catalog import BASE_TITLES, CONTRACTS, TITLES, PRIMARY

ROOT = Path(__file__).resolve().parents[1]
PROTECTED = ("package.json", "package-lock.json", "tsconfig.json", "frontend/vite.config.ts", "yolocoder.json", "scripts/start.sh", "scripts/stop.sh", "scripts/restart.sh")


def replace(root, path, before, after):
    file = root / path
    text = file.read_text()
    if text.count(before) != 1:
        raise ValueError(f"Fixture mutation must match once: {path}: {before!r}")
    file.write_text(text.replace(before, after))


def seed_bug(root, app):
    changes={
      'csv':('frontend/src/App.tsx','cell.toLowerCase().includes(query.toLowerCase())','cell.includes(query)'),
      'quiz':('backend/index.ts','if(a.choice===q.correctIndex)','if(a.choice && a.choice===q.correctIndex)'),
      'inventory':('backend/index.ts','const quantity=row.quantity+delta','const quantity=Math.max(0,row.quantity+delta)'),
      'notes':('backend/index.ts','body===undefined?old.body:body',"body===undefined?'':body"),
      'application':('frontend/src/App.tsx','function back(){S(step-1);}',"function back(){if(step===3)D({...data,statement:''});S(step-1);}"),
    }
    replace(root,*changes[app])

def feature_solution(root,app):
    from .solutions import apply
    apply(root,app,replace)


def hashes(root, paths=PROTECTED):
    return {p: hashlib.sha256((root/p).read_bytes()).hexdigest() if (root/p).is_file() else None for p in paths}


def seed_existing_data(destination, app):
    schemas={
      'csv': "CREATE TABLE datasets (id INTEGER PRIMARY KEY,name TEXT NOT NULL,columns TEXT NOT NULL,rows TEXT NOT NULL); INSERT INTO datasets VALUES (1,'Existing dataset','[\"Name\",\"Value\"]','[[\"Original\",\"07\"]]');",
      'quiz': "CREATE TABLE questions (id INTEGER PRIMARY KEY,prompt TEXT NOT NULL,options TEXT NOT NULL,correctIndex INTEGER NOT NULL); INSERT INTO questions VALUES (1,'Existing question','[\"Old A\",\"Old B\"]',0);",
      'inventory': "CREATE TABLE items (id INTEGER PRIMARY KEY,sku TEXT NOT NULL COLLATE NOCASE UNIQUE,name TEXT NOT NULL,quantity INTEGER NOT NULL); INSERT INTO items VALUES (1,'OLD-001','Existing item',7);",
      'notes': "CREATE TABLE notes (id INTEGER PRIMARY KEY,title TEXT NOT NULL,body TEXT NOT NULL); INSERT INTO notes VALUES (1,'Existing note','Original body <b>literal</b>');",
      'application': "CREATE TABLE applications (id INTEGER PRIMARY KEY,name TEXT NOT NULL,email TEXT NOT NULL,track TEXT NOT NULL,statement TEXT NOT NULL,status TEXT NOT NULL DEFAULT 'draft'); INSERT INTO applications VALUES (1,'Existing applicant','old@example.com','Design','Original submitted statement','submitted');",
    }
    if app in schemas:
        with sqlite3.connect(destination/'backend/data.db') as db: db.executescript(schemas[app])


def materialize(task, destination, control="start"):
    if destination.exists():
        raise FileExistsError(destination)
    shutil.copytree(ROOT / "fixtures/common", destination)
    if task.kind != "create" or control == "solution":
        shutil.copytree(ROOT / "fixtures" / task.app, destination, dirs_exist_ok=True)
    if task.kind == "bug" and control != "solution":
        seed_bug(destination, task.app)
    if control == "solution" and task.kind == "feature":
        feature_solution(destination, task.app)
    if control == "solution" and task.kind == "edit":
        replace(destination,"frontend/src/App.tsx", '<h1>'+BASE_TITLES[task.app]+'</h1>', '<h1>'+TITLES[task.app]+'</h1>')
        import re
        file=destination/'frontend/src/App.tsx'
        text=file.read_text()
        target=re.compile(r'(<button)([^<]*)(>'+re.escape(PRIMARY[task.app])+r'</button>)')
        text,count=target.subn(r'\1 style={{backgroundColor:"#7c3aed"}}\2\3',text)
        if count!=1:raise ValueError('Primary edit must match one button')
        file.write_text(text)
    (destination / "ARCHITECTURE.md").write_text("# Project contract\n\n" + CONTRACTS[task.app] + "\n\nFrontend: frontend/src/App.tsx. Backend: backend/index.ts. SQLite: backend/db.ts. npm test checks TypeScript. Ports come from FRONTEND_PORT and BACKEND_PORT.\n")
    import re
    for file in destination.rglob('*'):
        if file.suffix in ('.ts','.tsx'):
            file.write_text(re.sub(r'/\*[A-Z_]+\*/','',file.read_text()))
    runtime = ROOT / ".cache/runtime"
    (destination / "node_modules").symlink_to(runtime / "node_modules", target_is_directory=True)
    if task.kind!='create': seed_existing_data(destination,task.app)
    return hashes(destination)


def suite_digest():
    from .catalog import tasks
    digest = hashlib.sha256(json.dumps([t.to_dict() for t in tasks()], sort_keys=True).encode())
    for base in (ROOT/"fixtures", ROOT/"browser", ROOT/"yoloeval"):
        for path in sorted(base.rglob('*')):
            if path.is_file() and '__pycache__' not in path.parts:
                digest.update(str(path.relative_to(ROOT)).encode()); digest.update(path.read_bytes())
    return digest.hexdigest()


def dependency_digest():
    digest = hashlib.sha256()
    for base in (ROOT/'node_modules', ROOT/'.cache/runtime/node_modules'):
        if not base.is_dir(): raise FileNotFoundError(f'Missing dependencies: {base}')
        for path in sorted(base.rglob('*')):
            if any(part in ('.vite','.vite-temp') for part in path.relative_to(base).parts):continue
            if path.is_file():
                digest.update(str(path.relative_to(ROOT)).encode())
                digest.update(path.read_bytes())
    return digest.hexdigest()
