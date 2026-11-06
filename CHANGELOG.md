# Changelog

## v0.4.0 - 2026-11-06

### Added

- Short memory IDs, strict instance/project/client binding and compact SDD recall
  rows. The recall date, ID and note schema is unchanged by capture auditing.
- Opt-in model-based capture with visible fact/preference/correction/decision
  categories, trust levels and add/reinforce/supersede/skip reasons.
- A private local SQLite capture audit, including redacted provider requests,
  model responses, token usage, latency, parse rejections and action decisions.
  `muninn capture log` browses runs and `muninn capture stats` summarizes them.
  Retention defaults to 30 days via `extract.audit_days`. Audit data never syncs.
- Bounded first-hook backlog, resumable message fragments and manual `--backfill`.
  Hook failures remain silent to the harness, with safe failure classes in logs.
- Independent project-memory sharing on branch `muninn-notes`, mounted at
  `.muninn-notes/`, using `github.com/veridit/huginn/gitbranch`. Requires Git 2.42
  or newer for Git-backed sharing. No memory commits pollute the code branch.
- Explicit, idempotent `muninn sync --migrate` with `--dry-run`, preserving project
  and client markers while moving legacy tracked notes off the code branch.

### Fixed

- Duplicate nearest-neighbor edges under repeated or interrupted indexing.
- Capped embedding passes now converge to a complete deterministic neighbor
  graph when followed by a full pass.
- Capture checkpoints completed chunks before later provider timeouts, bounds
  each hook request and classifies index/provider/timeout failures safely.

### Installation

Use Homebrew (`brew install veridit/tap/muninn`), the installer (`install.sh`),
Scoop, or the six platform release archives. Recall works offline without model
credentials. Optional capture requires a configured provider and explicit opt-in.
