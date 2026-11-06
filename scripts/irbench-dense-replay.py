#!/usr/bin/env python3
"""CPU-only replay after query cache preparation under a coordinated GPU lease."""
import json, os, pathlib, sqlite3, subprocess
root = pathlib.Path(os.environ['JCODE_SCRATCH_DIR'])
folder = root / 'irbench' / 'replay'
cache = folder / 'dense-development-cache.json'
assert cache.exists(), 'prepare leased query cache before running offline replay'
messages = json.loads((root / 'recall3' / 'replay.json').read_text())
split = 'development'
for variant in ['maxsim', 'rrf']:
    output = folder / f'{variant}-{split}.jsonl'
    assert not output.exists(), f'refusing unsafe overwrite: {output}'
    db = folder / f'{variant}-{split}.db'
    source = sqlite3.connect(f'file:{root}/recall2/muninn.db?mode=ro', uri=True)
    target = sqlite3.connect(db)
    source.backup(target)
    target.execute('delete from injections')
    target.commit()
    target.close()
    source.close()
    print('JCODE_PROGRESS ' + json.dumps({'message': f'{variant} {split} start'}), flush=True)
    with output.open('w') as out, (folder / f'{variant}-{split}.log').open('w') as log:
        subprocess.run([str(folder / 'recalleval'), '-db', str(db), '-input', str(root / 'recall3' / 'replay.json'), '-split', split, '-variant', variant, '-query-cache', str(cache)], stdout=out, stderr=log, check=True)
    rows = [json.loads(line) for line in output.read_text().splitlines()]
    assert [r['id'] for r in rows] == [m['id'] for m in messages if m['split'] == split]
    assert not any(row['error'] for row in rows)
    print('JCODE_PROGRESS ' + json.dumps({'message': f'{variant} complete rows={len(rows)} errors=0'}), flush=True)
