#!/usr/bin/env python3
"""Build frozen actors, validate the grader offline, then run a fresh paid A/B."""
import argparse
import hashlib
import json
import os
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent
REPO = ROOT.parent
sys.path.insert(0, str(ROOT))
from yoloeval.fixtures import dependency_digest, suite_digest
from yoloeval.runner import write_json


def run(argv, cwd=REPO):
    subprocess.run([str(x) for x in argv], cwd=cwd, check=True,
                   env={**os.environ, 'PYTHONPATH': str(ROOT), 'YOLOCODER_NO_AUTOUPDATE': '1'})


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def actor_hashes(out):
    receipt = json.loads((out/'actors.json').read_text())
    for role, name in [('baseline', 'vanilla'), ('candidate', 'winner')]:
        if sha(out/'binaries'/name) != receipt[role]['binary_sha256']:
            raise ValueError('Actor changed since preparation: '+role)
    return {role: receipt[role]['binary_sha256'] for role in ('baseline', 'candidate')}


def prepare(out):
    # Never overwrite a binary or source tree from an existing campaign.
    (out/'binaries').mkdir(parents=True, exist_ok=False)
    specs = json.loads((ROOT/'actors.json').read_text())
    receipt = {}
    go = os.environ.get('GO', 'go')
    toolchain = subprocess.check_output([go, 'version'], text=True).strip()
    for role, name in [('baseline', 'vanilla'), ('candidate', 'winner')]:
        spec = specs[role]
        source = out/'sources'/name
        source.mkdir(parents=True)
        # git archive excludes credentials, local state, and experimental history.
        archive = subprocess.check_output(['git', 'archive', spec['revision']], cwd=REPO)
        subprocess.run(['tar', '-xf', '-', '-C', str(source)], input=archive, check=True)
        actual = {str(p.relative_to(source)): sha(p)
                  for base in ('cmd', 'internal') for p in (source/base).rglob('*') if p.is_file()}
        actual.update({p: sha(source/p) for p in ('go.mod', 'go.sum')})
        if actual != spec['source_sha256']:
            raise ValueError('Published actor source differs from the frozen winner: '+role)
        flags = spec['flags'] + ['-X', 'github.com/mindsdb/yolocoder/internal/version.Version='+spec['version'],
                                '-X', 'github.com/mindsdb/yolocoder/internal/version.Commit='+spec['version_commit']]
        binary = out/'binaries'/name
        run([go, 'build', '-buildvcs=false', '-ldflags', ' '.join(flags), '-o', binary, './cmd/yolocoder'], source)
        receipt[role] = {'revision': spec['revision'], 'source_sha256': spec['source_sha256'],
                         'binary_sha256': sha(binary), 'go': toolchain, 'ldflags': flags}
    write_json(out/'actors.json', receipt)
    print('Frozen sources verified and both actors built. No paid calls made.')


def check(out):
    hashes = actor_hashes(out)
    calibration = out/'calibration'
    calibration.mkdir(exist_ok=False)
    before = {'suite_digest': suite_digest(), 'dependency_digest': dependency_digest(),
              'binary_sha256': hashes}
    write_json(calibration/'start-freeze.json', before)
    run([sys.executable, '-m', 'unittest', 'discover', '-s', ROOT/'tests', '-v'])
    run(['node', '--test', ROOT/'tests/test_controls.mjs'])
    run([sys.executable, '-m', 'yoloeval.cli', 'validate', '--binary', out/'binaries/vanilla', '--output', calibration])
    run([sys.executable, ROOT/'validation/runtime.py', '--binary', out/'binaries/vanilla', '--output', out/'runtime-check'])
    result = json.loads((calibration/'validation.json').read_text())
    if not result['passed'] or len(result['controls']) != 40:
        raise ValueError('Need all 40 valid controls')
    after = {'suite_digest': suite_digest(), 'dependency_digest': dependency_digest(),
             'binary_sha256': actor_hashes(out)}
    if before != after:
        raise ValueError('Inputs changed during offline validation')
    write_json(calibration/'freeze.json', {**after, 'calibration_passed': True})
    print('Offline validation passed. The paid campaign is a separate, explicit run step.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('step', choices=['setup', 'prepare', 'check', 'run'])
    parser.add_argument('--output', type=Path, default=ROOT/'runs/replication')
    args = parser.parse_args()
    out = args.output.expanduser().resolve()
    if args.step == 'setup':
        run([sys.executable, '-m', 'yoloeval.cli', 'setup'])
    elif args.step == 'prepare':
        prepare(out)
    elif args.step == 'check':
        check(out)
    else:
        actor_hashes(out)
        run([sys.executable, '-u', '-m', 'yoloeval.unseen', '--output', out/'comparison',
             '--calibration', out/'calibration/validation.json', '--binaries', out/'binaries',
             '--actors', out/'actors.json'])


if __name__ == '__main__':
    main()
