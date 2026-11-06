#!/usr/bin/env python3
"""CPU arms using tiger's approved per-split, fresh-injection replay protocol.
No judging or heldout parameter selection. All outputs stay under irbench.
"""
import json, os, pathlib, sqlite3, subprocess
root = pathlib.Path(os.environ['JCODE_SCRATCH_DIR'])
outdir = root / 'irbench' / 'replay'
outdir.mkdir(parents=True, exist_ok=True)
messages = json.loads((root / 'recall3' / 'replay.json').read_text())
for split in ['development', 'heldout']:
    for variant in ['bm25', 'idf24', 'adaptive', 'idf24-adaptive']:
        output = outdir / f'{variant}-{split}.jsonl'
        if output.exists():
            raise RuntimeError(f'refusing overwrite or unsafe resume: {output}')
        dbpath = outdir / f'{variant}-{split}.db'
        src = sqlite3.connect(f'file:{root}/recall2/muninn.db?mode=ro', uri=True)
        dst = sqlite3.connect(dbpath)
        src.backup(dst)
        dst.execute('delete from injections')
        dst.commit()
        dst.close()
        src.close()
        expected = sum(m['split'] == split for m in messages)
        print('JCODE_PROGRESS ' + json.dumps({'message': f'{variant} {split} start expected={expected}'}), flush=True)
        with output.open('w') as out, (outdir / f'{variant}-{split}.log').open('w') as log:
            subprocess.run([str(outdir / 'recalleval'), '-db', str(dbpath), '-input', str(root / 'recall3' / 'replay.json'), '-split', split, '-variant', variant], stdout=out, stderr=log, check=True)
        rows = [json.loads(x) for x in output.read_text().splitlines()]
        assert len(rows) == expected and len({r['id'] for r in rows}) == expected
        assert not [r for r in rows if r['error']]
        print('JCODE_PROGRESS ' + json.dumps({'message': f'{variant} {split} complete rows={len(rows)} errors=0'}), flush=True)
