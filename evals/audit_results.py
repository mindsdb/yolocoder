#!/usr/bin/env python3
"""Audit the published 120-attempt historical result without API calls."""
import hashlib
import json
import math
import statistics
from pathlib import Path

from yoloeval.catalog import tasks
from yoloeval.scoring import quantile
from yoloeval.unseen import decision

ROOT = Path(__file__).resolve().parent/'results/unseen-002'


def read_rows(name):
    rows = [json.loads(line) for line in (ROOT/name).read_text().splitlines()]
    keys = [(row['role'], row['task_id'], row['trial']) for row in rows]
    expected = {(role, task.id, trial) for role in ('baseline', 'candidate')
                for task in tasks() for trial in (1, 2, 3)}
    if len(keys) != len(expected) or set(keys) != expected:
        raise ValueError('Missing, duplicate or unexpected attempts in '+name)
    return {key: row for key, row in zip(keys, rows)}


def main():
    for line in (ROOT/'SHA256SUMS').read_text().splitlines():
        expected, name = line.split('  ', 1)
        if hashlib.sha256((ROOT/name).read_bytes()).hexdigest() != expected:
            raise ValueError('Published evidence changed: '+name)
    results, calls, changes = (read_rows(name) for name in ('results.jsonl', 'calls.jsonl', 'changes.jsonl'))
    rows = {role: [] for role in ('baseline', 'candidate')}
    for key, envelope in results.items():
        role, task, trial = key
        row = envelope['result']
        if (row['task_id'], row['trial']) != (task, trial) or row['status'] != 'completed':
            raise ValueError('Incomplete or misidentified result: '+str(key))
        if row['passed'] != (not row['agent_error'] and all(c['passed'] for c in row['checks'])):
            raise ValueError('Pass flag differs from checks/error: '+str(key))
        trace = calls[key]['calls']
        if row['usage']['model_calls'] != len(trace) or not all(c['complete'] for c in trace):
            raise ValueError('Missing or undrained calls: '+str(key))
        allowed = {'muse-spark-1-3', 'jev-1.13.0'} if role == 'candidate' else {'muse-spark-1-3'}
        for call in trace:
            if call['requested_model'] not in allowed:
                raise ValueError('Unexpected model: '+str(key))
            if call['status'] == 200 and call.get('returned_model') != call['requested_model']:
                raise ValueError('Successful call returned a different model: '+str(key))
        rows[role].append(row)
    computed = decision(rows)
    recorded = json.loads((ROOT/'decision.json').read_text())
    for field in ('status', 'accepted', 'complete', 'counts', 'critical_regressions'):
        if computed[field] != recorded[field]:
            raise ValueError('Acceptance result differs: '+field)
    for field in ('candidate_mean_s', 'candidate_p50_s'):
        if not math.isclose(computed[field], recorded[field], abs_tol=1e-9):
            raise ValueError('Timing differs: '+field)
    summary = {}
    for role, samples in rows.items():
        durations = [r['timing']['agent_wall_s'] for r in samples]
        summary[role] = {'attempts': len(samples), 'passed': sum(r['passed'] for r in samples),
                         'mean_s': statistics.mean(durations), 'p50_s': quantile(durations, .5),
                         'p95_s': quantile(durations, .95)}
    print(json.dumps({'audit_passed': True, 'acceptance': computed['accepted'], 'summary': summary,
                      'scored_calls': sum(len(c['calls']) for c in calls.values()),
                      'preserved_patches': len(changes)}, indent=2))


if __name__ == '__main__':
    main()
