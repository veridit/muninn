#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Summarize primitive results and reuse existing blind labels. No new judge."""
import collections
import hashlib
import json
import os
import pathlib
import statistics

root = pathlib.Path(os.environ['JCODE_SCRATCH_DIR'])
out = root / 'irbench'
def rows(name):
    return [json.loads(line) for line in (out/name).read_text().splitlines()]
def latency(data):
    values = sorted(r['latency_ms'] for r in data if 'latency_ms' in r)
    return {'n': len(values), 'p50_ms': statistics.median(values), 'p95_ms': values[int(.95*(len(values)-1))]} if values else {}
summary = {}
for name in ['rerank-cpu.jsonl', 'rerank-mps.jsonl']:
    rs = rows(name)
    summary[name] = {**latency(rs), 'startup': rs[0]}
dense = collections.defaultdict(list)
for r in rows('dense.jsonl'):
    dense[r['variant']].append(r)
summary['dense'] = {v: latency(rs) for v,rs in dense.items()}
for stem in ['single', 'maxsim']:
    exact = {r['id']: {h['id'] for h in r['hits']} for r in dense[stem+'-exact']}
    overlaps = [len(exact[r['id']] & {h['id'] for h in r['hits']})/10 for r in dense[stem+'-sketch']]
    summary['dense'][stem+'-sketch']['exact_top10_overlap'] = statistics.mean(overlaps)
lex = collections.defaultdict(list)
for r in rows('lexical.jsonl'):
    if r['repeat'] == 0:
        lex[r['variant']].append(r)
summary['lexical_oracle_overlap'] = {v: statistics.mean(r['oracle_overlap_at_10']/r['oracle_size'] if r['oracle_size'] else 1 for r in rs) for v,rs in lex.items()}

qs = {q['id']: q for q in json.loads((root/'recall3/longmem-subset.json').read_text())}
lme = collections.defaultdict(list)
for r in rows('longmem-cpu.jsonl'):
    lme[r['variant']].append(r)
summary['longmem'] = {}
for variant,rs in lme.items():
    positive = [r for r in rs if r['evidence_total']>0]
    negative = [r for r in rs if r['evidence_total']==0]
    reciprocal = []
    at5 = []
    for r in positive:
        ev = {i+1 for i,t in enumerate(qs[r['question_id']]['rounds']) if t['evidence']}
        ranks = [i+1 for i,h in enumerate(r['hits'] or []) if h['id'] in ev]
        reciprocal.append(1/min(ranks) if ranks else 0)
        at5.append(any(rank<=5 for rank in ranks))
    summary['longmem'][variant] = {**latency(rs), 'questions':len(rs), 'answerable':len(positive), 'unanswerable':len(negative), 'evidence_hit_at10':statistics.mean(r['evidence_found']>0 for r in positive), 'evidence_hit_at5':statistics.mean(at5), 'evidence_mrr_at10':statistics.mean(reciprocal), 'macro_evidence_recall_at10':statistics.mean(r['evidence_found']/r['evidence_total'] for r in positive), 'macro_session_recall_at10':statistics.mean(r['sessions_found']/r['sessions_total'] for r in positive if r['sessions_total']), 'abstention_specificity':statistics.mean(not r['hits'] for r in negative) if negative else None, 'returned':sum(len(r['hits'] or []) for r in rs)}

# Filter-only counterfactual: same already-authorized baseline hits, no replay of
# changed chronological injection state. All labels must exist, no unknown=false.
path = out/'baseline-rerank-cpu.jsonl'
if path.exists() and len(path.read_text().splitlines())==101:
    scored = {r['id']:r['scores'] for r in rows(path.name) if 'scores' in r}
    replay = {m['id']:m for m in json.loads((root/'recall2/replay.json').read_text())}
    labels = json.loads((root/'recall2/judgments.json').read_text())
    baseline = [json.loads(line) for line in (root/'recall2/old-dev-v3.jsonl').read_text().splitlines()]
    quality = {}
    for threshold in [None,0,2,4,6]:
        by = collections.defaultdict(lambda: {'messages':0, 'returned':0, 'relevant':0, 'empty':0})
        for r in baseline:
            stratum = replay[r['id']]['stratum']
            hits = r['response'].get('hits') or []
            scores = scored.get(r['id'],[])
            if len(hits)!=len(scores):
                raise ValueError('score/hit count mismatch')
            retained = [h for h,s in zip(hits,scores) if threshold is None or s>=threshold]
            for k in ['all',stratum]:
                by[k]['messages']+=1
                by[k]['returned']+=len(retained)
                by[k]['empty']+=not retained
                for h in retained:
                    key=hashlib.sha256((str(r['id'])+'\0'+h['text']).encode()).hexdigest()[:20]
                    by[k]['relevant']+=labels[key]['relevant']
        for v in by.values():
            v['precision']=v['relevant']/v['returned'] if v['returned'] else None
            v['relevant_per_message']=v['relevant']/v['messages']
        quality['baseline' if threshold is None else f'gate{threshold}']=dict(by)
    summary['blind_dev_filter_counterfactual']=quality
(out/'summary.json').write_text(json.dumps(summary,indent=2))
print(json.dumps(summary,indent=2))
