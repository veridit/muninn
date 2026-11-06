#!/usr/bin/env python3
# SPDX-License-Identifier: MPL-2.0
"""Optional local inference worker for the Go irbench Reranker adapter.

Install sentence-transformers into a scratch-only venv. Set HF_HOME under scratch.
No trust_remote_code, no cloud inference, no memory data uploaded. Model files
are downloaded by the caller's explicit invocation. JSONL stdin/stdout protocol.
"""
import argparse
import contextlib
import json
import sys
import time

p = argparse.ArgumentParser()
p.add_argument('--model', default='cross-encoder/ms-marco-MiniLM-L6-v2')
p.add_argument('--device', default='cpu', choices=['cpu', 'mps'])
p.add_argument('--threads', type=int, default=4)
a = p.parse_args()
with contextlib.redirect_stdout(sys.stderr):
    import torch
    from sentence_transformers import CrossEncoder
    torch.set_num_threads(a.threads)
    started = time.perf_counter()
    model = CrossEncoder(a.model, device=a.device, max_length=512, trust_remote_code=False)
    print(json.dumps({'startup_ms': (time.perf_counter()-started)*1000, 'model': a.model, 'device': a.device}), file=sys.stderr, flush=True)
for line in sys.stdin:
    try:
        req = json.loads(line)
        pairs = [[req['query'], doc] for doc in req['documents']]
        started = time.perf_counter()
        with contextlib.redirect_stdout(sys.stderr):
            scores = model.predict(pairs, batch_size=16, show_progress_bar=False).tolist() if pairs else []
        print(json.dumps({'scores': scores, 'latency_ms': (time.perf_counter()-started)*1000}), flush=True)
    except Exception as exc:
        print(json.dumps({'error': str(exc)}), flush=True)
