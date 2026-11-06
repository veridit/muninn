#!/usr/bin/env python3
"""Audit replay shape, IDs, error count and visible response scopes, no judging."""
import collections, json, os, pathlib, statistics
root = pathlib.Path(os.environ['JCODE_SCRATCH_DIR'])
folder = root / 'irbench' / 'replay'
inputs = json.loads((root / 'recall3' / 'replay.json').read_text())

def allowed(value, rules):
    positive = matched = False
    for entry in rules:
        deny = entry.startswith('!')
        rule = entry[1:] if deny else entry
        match = rule == value or (rule.endswith('*') and value.startswith(rule[:-1]))
        if deny and match:
            return False
        if not deny:
            positive = True
            matched = matched or match
    return not positive or matched

summary = {}
for split in ['development', 'heldout']:
    expected = [m['id'] for m in inputs if m['split'] == split]
    variants = ['bm25', 'idf24', 'adaptive', 'idf24-adaptive']
    if split == 'development':
        variants += [v for v in ['maxsim', 'rrf'] if (folder / f'{v}-{split}.jsonl').exists()]
    for variant in variants:
        file = folder / f'{variant}-{split}.jsonl'
        rows = [json.loads(line) for line in file.read_text().splitlines()]
        assert [r['id'] for r in rows] == expected
        assert not any(r['error'] for r in rows)
        hits = [hit for row in rows for hit in row['response']['hits'] or []]
        for row in rows:
            for hit in row['response']['hits'] or []:
                assert allowed(hit['scope'], row['scopes'] or []), (file.name, row['id'], hit['scope'])
        times = sorted(r['latency_ms'] for r in rows)
        summary[file.stem] = dict(messages=len(rows), returned=len(hits), empty=sum(not r['response']['hits'] for r in rows), errors=0, scope_violations=0, p50_ms=statistics.median(times), p95_ms=times[int(.95*(len(times)-1))], skipped=dict(collections.Counter(r['response'].get('skipped', '') for r in rows)))
        if variant in ['maxsim', 'rrf']:
            cpu = sorted(r['retrieval_cpu_ms'] for r in rows)
            embedding = sorted(r['embedding_ms'] for r in rows)
            summary[file.stem]['cpu_p50_ms'] = statistics.median(cpu)
            summary[file.stem]['cpu_p95_ms'] = cpu[int(.95*(len(cpu)-1))]
            summary[file.stem]['embedding_p50_ms'] = statistics.median(embedding)
            summary[file.stem]['embedding_p95_ms'] = embedding[int(.95*(len(embedding)-1))]
            assert all(r['cached_embedding'] for r in rows)
            assert all(abs(r['latency_ms'] - r['retrieval_cpu_ms'] - r['embedding_ms']) < 1e-6 for r in rows)
(folder / 'audit.json').write_text(json.dumps(summary, indent=2))
print(json.dumps(summary, indent=2))
