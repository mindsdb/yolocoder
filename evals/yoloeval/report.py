import html
import json
from pathlib import Path
from .scoring import summarize


def load_results(folder):
    return [json.loads(p.read_text()) for p in sorted(folder.glob('*/result.json'))]


def display(value, digits=2):
    return 'unavailable' if value is None else f'{value:,.{digits}f}'


def report(folder, rows=None):
    rows=load_results(folder) if rows is None else rows
    summary=summarize(rows)
    by_kind={kind:summarize([r for r in rows if r['kind']==kind]) for kind in ('create','edit','feature','bug')}
    (folder/'summary.json').write_text(json.dumps({'overall':summary,'by_kind':by_kind},indent=2))
    lines=['# YoloCoder evaluation report','',f"**{summary['passed']}/{summary['scheduled_results']} scheduled attempts passed**; {summary['attempts']} model-evaluable attempts and {summary['infra_errors']} infrastructure errors.",'',
        f"Agent time, all completed attempts: median **{display(summary['agent_p50_s_all'])}s**, empirical p95 **{display(summary['agent_p95_s_all'])}s**.",
        f"Successful attempts: agent median {display(summary['agent_p50_s_success'])}s; independent verified completion median {display(summary['verified_p50_s_success'])}s.",
        f"Recorded agent seconds per success, including scored failures: {display(summary['agent_seconds_per_success_including_failures'])}s. Infrastructure-error time is excluded where unavailable.",
        f"Reported tokens including failures: {summary['reported_tokens_including_failures']:,}. Usage complete: {summary['usage_complete']}. Cost: {'unavailable (no complete supplied price schedule)' if summary['cost_usd'] is None else '$'+display(summary['cost_usd'],4)}.",'',
        'Setup and dependency installation are excluded from warm-turn latency. Verified completion includes independent browser/API grading. First-correct-preview latency is not measured. With fewer than 100 observations, p95 is descriptive only; do not market it as a stable tail estimate.','',
        '| Task | Trial | Result | Agent seconds | Tokens | Failed checks |','|---|---:|---|---:|---:|---|']
    table=[]
    for r in sorted(rows,key=lambda x:(x['task_id'],x['trial'])):
        failures=', '.join(c['id'] for c in r.get('checks',[]) if not c['passed']) or r.get('infra_error') or r.get('agent_error') or '—'
        timing=r.get('timing',{}).get('agent_wall_s');tokens=r.get('usage',{}).get('tokens_reported',{}).get('total',0)
        result='PASS' if r['passed'] else 'INFRA ERROR' if r['status']=='infra_error' else 'FAIL'
        lines.append(f"| {r['task_id']} | {r['trial']} | {result} | {display(timing)} | {tokens:,} | {failures.replace('|','/')} |")
        relative=f"{r['task_id']}--{r['trial']}"
        links=[]
        for label,file in (('JSON','result.json'),('Diff','changes.diff'),('Screenshot','evidence/desktop.png'),('Jev review','jev-review.json')):
            href=f'{relative}/{file}'
            if (folder/href).is_file():links.append(f'<a href="{html.escape(href,quote=True)}">{label}</a>')
        table.append('<tr>'+''.join(f'<td>{html.escape(str(x))}</td>' for x in (r['task_id'],r['trial'],result,display(timing),f'{tokens:,}',failures))+f'<td>{" · ".join(links)}</td></tr>')
    (folder/'report.md').write_text('\n'.join(lines)+'\n')
    body=f'''<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>YoloCoder evals</title>
<style>body{{font:15px system-ui;margin:40px;color:#182c3b;background:#f7f8fa}}h1{{font-size:32px}}.metrics{{font-size:22px}}table{{border-collapse:collapse;background:white;width:100%}}td,th{{text-align:left;padding:10px;border-bottom:1px solid #ddd}}td:last-child{{white-space:nowrap}}.note{{max-width:1000px;color:#52616b;line-height:1.6}}a{{color:#1756ab}}</style>
<h1>YoloCoder evaluations</h1><p class="metrics">{summary['passed']}/{summary['scheduled_results']} scheduled attempts passed · {display(summary['agent_p50_s_all'])}s median recorded agent time · {summary['reported_tokens_including_failures']:,} reported tokens</p>
<p class="note">Correctness is a hard gate. Failed attempts count toward time and token efficiency. {summary['infra_errors']} infrastructure errors. Warm project setup is excluded. Independent verification time is separate. First-correct-preview latency is not measured. Cost is unavailable unless a complete price schedule was supplied. This small sample does not establish stable p95 latency.</p>
<table><thead><tr><th>Task</th><th>Trial</th><th>Result</th><th>Agent seconds</th><th>Tokens</th><th>Failed checks</th><th>Evidence</th></tr></thead><tbody>{''.join(table)}</tbody></table></html>'''
    (folder/'report.html').write_text(body)
    return summary
