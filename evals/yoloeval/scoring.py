"""Correctness gates selection. Speed cannot buy back a failed requirement."""
import math
import random
import statistics

GROUP_WEIGHTS = {'acceptance':60, 'regression':30, 'usability':10}


def score(checks, agent_error=None):
    groups = {}
    for group, weight in GROUP_WEIGHTS.items():
        selected=[c for c in checks if c['group']==group]
        groups[group]={'passed':sum(c['passed'] for c in selected),'total':len(selected),'weight':weight}
    denominator=sum(g['weight'] for g in groups.values() if g['total'])
    quality=sum(g['weight']*g['passed']/g['total'] for g in groups.values() if g['total'])/denominator*100 if denominator else 0
    return {'passed':bool(checks) and not agent_error and all(c['passed'] for c in checks if c.get('critical',True)),
            'quality_score':round(quality,2),'groups':groups}


def quantile(values, fraction):
    if not values:return None
    ordered=sorted(values)
    return ordered[min(len(ordered)-1,max(0,math.ceil(len(ordered)*fraction)-1))]


def wilson(passed,total):
    if not total:return [0,1]
    z=1.959963984540054;p=passed/total;d=1+z*z/total
    center=(p+z*z/(2*total))/d
    spread=z*math.sqrt(p*(1-p)/total+z*z/(4*total*total))/d
    return [max(0,center-spread),min(1,center+spread)]


def summarize(results):
    attempts=[r for r in results if r.get('status')!='infra_error']
    good=[r for r in attempts if r['passed']]
    agent=[r['timing']['agent_wall_s'] for r in attempts if r.get('timing',{}).get('agent_wall_s') is not None]
    success_agent=[r['timing']['agent_wall_s'] for r in good if r.get('timing',{}).get('agent_wall_s') is not None]
    verified=[r['timing']['verified_wall_s'] for r in good if r.get('timing',{}).get('verified_wall_s') is not None]
    usage_complete=all(r.get('usage',{}).get('usage_complete',False) for r in results)
    token_total=sum(r.get('usage',{}).get('tokens_reported',{}).get('total',0) for r in results)
    costs=[r.get('cost_usd') for r in results]
    return {'scheduled_results':len(results),'infra_errors':len(results)-len(attempts),'attempts':len(attempts),
            'passed':len(good),'success_rate':len(good)/len(attempts) if attempts else None,
            'success_rate_95ci':wilson(len(good),len(attempts)),
            'agent_p50_s_all':quantile(agent,.5),'agent_p95_s_all':quantile(agent,.95),
            'agent_p50_s_success':quantile(success_agent,.5),'verified_p50_s_success':quantile(verified,.5),
            'tail_sample_warning':len(agent)<100,
            'agent_seconds_per_success_including_failures':sum(agent)/len(good) if good else None,
            'reported_tokens_including_failures':token_total,'usage_complete':usage_complete,
            'reported_tokens_per_success_including_failures':token_total/len(good) if good else None,
            'cost_usd':sum(costs) if all(c is not None for c in costs) else None}


def compare(baseline,candidate,seed=137):
    def keyed(rows):
        result={}
        for r in rows:
            key=(r['task_id'],r['trial'])
            if key in result:raise ValueError(f'Duplicate paired observation: {key}')
            result[key]=r
        return result
    left,right=keyed(baseline),keyed(candidate)
    if left.keys()!=right.keys():raise ValueError('Comparisons require identical task/trial sets, including failures')
    if any(r.get('status')=='infra_error' for r in baseline+candidate):raise ValueError('Resolve infrastructure errors before comparing candidates')
    regressions=[k for k in left if left[k]['passed'] and not right[k]['passed']]
    improvements=[k for k in left if not left[k]['passed'] and right[k]['passed']]
    paired=[k for k in left if left[k]['passed'] and right[k]['passed']]
    # Bootstrap whole tasks, not individual repeats: repetitions of an app
    # task are correlated and must not masquerade as independent coverage.
    by_task={}
    for task,trial in paired:
        a=left[(task,trial)]['timing']['agent_wall_s'];b=right[(task,trial)]['timing']['agent_wall_s']
        if a>0 and b>0:by_task.setdefault(task,[]).append(math.log(a/b))
    task_effects=[statistics.mean(v) for v in by_task.values()]
    ratios=[];rng=random.Random(seed)
    if task_effects:
        for _ in range(2000):ratios.append(math.exp(statistics.mean(rng.choices(task_effects,k=len(task_effects)))))
    ci=[quantile(ratios,.025),quantile(ratios,.975)] if ratios else [None,None]
    enough=len(by_task)>=5 and min((sum(k[0]==t for k in left) for t in by_task),default=0)>=3
    return {'baseline':summarize(baseline),'candidate':summarize(candidate),
            'regressions':[list(k) for k in regressions],'improvements':[list(k) for k in improvements],
            'joint_success_pairs':len(paired),'speedup_geomean':math.exp(statistics.mean(task_effects)) if task_effects else None,
            'speedup_95ci_task_bootstrap':ci,'enough_repeats_for_screening':enough,
            'promotion_candidate':not regressions and enough and bool(ci[0] and ci[0]>1),
            'promotion_note':'A screening result, not automatic promotion. Inspect correctness and reserved holdout before accepting.'}
