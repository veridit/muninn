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
    for variant in ['bm25', 'idf24', 'adaptive', 'idf24-adaptive']:
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
(folder / 'audit.json').write_text(json.dumps(summary, indent=2))
print(json.dumps(summary, indent=2))
