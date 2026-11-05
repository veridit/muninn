# Passage recall (candidate)

Recall treats all messages the same, without sender-specific rules.

- Preserve the existing query and semantic gates for messages up to 600 UTF-8
  bytes. Redundant quoted memories and learned boilerplate can still be skipped.
- Split longer messages on paragraphs and headings, keeping list blocks together
  where possible. Oversized blocks are cut at whitespace on a rune boundary.
- Bound preprocessing to 64 KiB and search at most eight passages, ordered by
  distinctiveness. Each passage has at most 24 keyword terms.
- Compute document frequencies over active units visible through the same scope
  and physical-source binding as retrieval. Ordinary terms qualify at document
  frequency at most 0.5% (minimum one document). Keep structured identifiers,
  including ticket IDs, CamelCase, and exact paths. Path components alone are
  not identifier evidence. The concept index supplies frequencies for long
  words, and FTS supplies frequencies for tokens omitted by concept extraction.
- Retrieve keyword candidates first, then embed selected passages in one batch.
  A hit needs either the configured semantic gate on one passage or exact
  overlap with one selected rare term. Use its best passage score, not the mean.
- Preserve scope filtering, canonical identity, session deduplication, score
  floor, and relative floor. Long messages return at most five hits without
  padding. Short messages retain their configured result limit.
- Suppress notes whose five-word shingles are at least 80% contained in the
  incoming message. Topic overlap alone is not duplication.
- Learn boilerplate after five distinct sessions in the same explicit binding.
  Only normalized, binding-salted fingerprints and hashed session identities
  are stored. Standalone numeric counts normalize, ticket identifiers do not.
  Anonymous calls do not train. The hash-only table is bounded to 4096 rows.
- Bound engine recall to 1.5 seconds, reserving finalization time and returning
  completed keyword results if embedding runs out of time. Cancellation also
  bounds a provider that ignores its context, with at most one lingering
  provider call per engine. Database reads use the retrieval deadline.
- `muninn recall --explain` prints each hit's passage and rare-term justification
  to stderr, leaving the injection block on stdout unchanged.

The deadline is an engine-level budget. App startup, source refresh, and harness
transport are outside it. No claim of a complete hook deadline follows from an
engine-only benchmark. Ordinary interactive `search` keeps its existing query
semantics.

## Acceptance

Only synthetic fixtures belong in the repository. Real transcript replays,
private golden queries, database copies, blind judgments, and reports belong in
`$JCODE_SCRATCH_DIR/recall2/`, never in Git. The candidate is not approved for
installation merely because unit tests pass: the independent replay precision,
relevant-hit retention, holdout, latency, and scope gates must also pass.
