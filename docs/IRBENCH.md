# Opt-in IR technique benchmarks

These commands are research tools, not production search defaults. All data,
model caches, snapshots and result files must live under
`$JCODE_SCRATCH_DIR/irbench/`. Do not commit benchmark inputs or results.

- `cmd/irbench`: warm lexical primitive timings, IDF/stoplist ablations and exact
  MaxScore versus exhaustive sparse BM25 oracle. Reads an explicit immutable
  SQLite snapshot and the existing replay JSON. Use `-split development`.
- `cmd/irdense`: existing float32 vectors, single-query versus passage MaxSim,
  exact versus per-passage binary shortlist rescoring. Embeds queries using the
  existing local Ollama configuration and stored pipeline prefix. Coordinate
  exclusive GPU/Ollama access with the shared replay owner before running.
- `cmd/irrerank`: persistent Go subprocess IPC, startup/warmup separately, warm
  neural reranker latency. Input JSONL: `id`, `query`, `documents`.
- `cmd/irlme`: consumes the shared, already prepared LongMemEval-S subset JSON
  from the replay lane. Does not download, resample, split, or copy the loader.
  Evaluates lexical candidates and optionally a local neural reranker.
- `scripts/irbench-reranker.py`: optional inference worker using
  `sentence-transformers` and PyTorch in a scratch-only virtual environment.
  Set `HF_HOME` inside scratch before invoking. Default model is
  `cross-encoder/ms-marco-MiniLM-L6-v2`, max length 512, batch 16, 4 CPU threads.
  Neither model inputs nor private documents are sent to a remote service.

The Go primitives are under `internal/irbench`. All ranking primitives are
explicit opt-ins. The eager BM25 corpus is an immutable benchmark index, not a
finished incremental persistence design. Title TF weight 2 plus one document
length normalizer is weighted BM25, not full BM25F. Passage MaxSim against one
vector per document is not trained token-level ColBERT.

## Correctness and interpretation

```
go test -race ./internal/irbench
go test ./cmd/irbench ./cmd/irdense ./cmd/irrerank ./cmd/irlme
```

MaxScore must match the exhaustive oracle for exactly the same selected query
terms, including deterministic ID tie-breaking and visibility predicates. IDF
term pruning and adaptive stoplists intentionally change the query and therefore
need independent relevance evaluation. RRF is a ranking operation, not a
probability or abstention policy. Neural logits are not calibrated probabilities.

Primitive timing excludes index loading/model startup. Global primitive scans
are not authorized bound recall output. Production evaluation must retain
binding scope, canonical dedup, session chronology and the blind relevance judge
in the existing shared replay harness rather than replacing them with this code.

## Authorized chronological callback follow-through

`cmd/recalleval` reuses tiger's approved replay command loop, adapted to this
branch's baseline API. An optional `recall.Engine.SearchFunc` replaces ranking
only, leaving own-output protection, request bindings, relative gates, rendering
and per-session content-hash injection tracking in the engine. Nil retains the
unchanged production Searcher. Callback tests verify bound-scope remapping before
a one-hit budget and injection dedup across repeated versus independent sessions.

`scripts/irbench-replay.py` uses the approved fresh-injection SQLite backup
protocol, exclusively on scratch copies of recall2/muninn.db. It consumes the
shared recall3/replay.json, without changing its ordering or split labels.
The four predeclared CPU-only arms are bm25, idf24, adaptive (DF>=.2), and
idf24-adaptive. They are **lexical-only replacements**, not hybrid-production
ablations. No Ollama calls occur in these arms. They retain configured MinScore
and RelativeFloor, but set no successful semantic signal, matching the engine's
keyword-only fallback gate behavior. BM25 scores are not calibrated to the
production keyword scale, so no threshold winner is asserted.

Canonical ownership and source visibility are checked before any candidate
budget. All visible scored candidates are sorted once, then SID/content-hash
dedup precedes the final limit. This intentionally prioritizes authorization
correctness over a bounded-candidate optimization. Startup corpus loading and
canonical checks are outside per-message latency. Original timestamps remain in
input but this baseline API has no recency signal enabled.

Run after building the command to `$JCODE_SCRATCH_DIR/irbench/replay/recalleval`:

```
python3 scripts/irbench-replay.py
```

The fixed development200 and heldout77 arms produce eight JSONL files under
scratch/irbench/replay, each with id/response/scopes/latency_ms/error/query/variant.
No judging is performed here. Tiger owns blind-cache evaluation and official
external metrics. The original annotation-positive external diagnostics exclude
only empty evidence, whereas official answerability excludes every `_abs`
question. Use the shared evaluator's 91-positive cohort, not the diagnostic94.
`cmd/irfusion` times fusion of saved same-snapshot candidate lists and
`scripts/irbench-summary.py` summarizes primitive diagnostics, not official QA.

### Dense chronological development arms

`cmd/recalleval -prepare-cache -query-cache <scratch-path>` embeds the selected
split's truncated, non-self-output queries in up to eight 600-byte passages.
Run this **only with the shared owner's exclusive Ollama lease**. Cache keys
are SHA256 of the same trimmed/truncated query used by the engine. Cache
metadata fixes the snapshot pipeline hash, query prefix and configured model.
Each completed query is atomically checkpointed to allow prompt lease release.

After releasing the lease, `scripts/irbench-dense-replay.py` runs MaxSim and
RRF on CPU against separate recall2 backups using this cache. No model calls
occur in dense callbacks. Canonical/source visibility is applied before each
passage's 1,024-entry Hamming budget, exact float rescoring follows, and
SID/content-hash dedup precedes each 20-candidate channel limit. RRF60 fuses
IDF24 weighted-BM2520 with passage-MaxSim20. Every fused candidate gets exact
maximum passage cosine for the engine's semantic gate, including lexical-only
candidates outside the dense channel shortlist.

These predeclared diagnostic policies disable absolute keyword MinScore,
because raw RRF is on an incompatible score scale. Configured semantic gates
and RelativeFloor remain enabled. MaxSim Total is 10*cosine; RRF Total is the
raw reciprocal-rank sum. Neither is a production-equivalent hybrid ablation.
The model is passage pooling against one stored vector, not token ColBERT.

Dense JSONL retains the approved response schema and additionally records
`retrieval_cpu_ms`, `embedding_ms` and `cached_embedding`. `latency_ms` is the
sum of actual CPU recall time and measured leased cache-preparation embedding
time for that query. It is a composed estimate, not contiguous end-to-end wall
time. Own-output skips charge zero embedding. Startup/index-loading is excluded.
The two arms share the same query cache but separate injection chronology.
Tests cover 1,100 excluded candidates preceding one bound candidate, cache miss,
model/pipeline mismatch, and query normalization without invoking a model.
