# muninn roadmap

Decided by the user on 2026-11-05. Completed items are marked below.

## Short ids, like huginn tasks (done 2026-11-05)

- [x] Every indexed memory gets a deterministic 6-char lowercase Crockford
  base32 id (`k7m2qx`), extended on collision. Unique prefixes of at least four
  characters resolve, as do permanent legacy ids.
- [x] Existing databases backfill `units.sid` automatically. The unique
  `memory_ids` registry maps sid to identity. `units.sid` is indexed, not unique:
  imported and live harness rows of the same memory share one sid.
  Upstream identity stays in `units.origin`, `sources`, and the original unit key.
  Native JSON stores `sid` without changing import idempotency or legacy ids.
- [x] `muninn show <id>` exposes source kind, path, key and origin. `--explain`
  exposes origin, not the ordinary injected recall block.
- [x] CLI, MCP, TUI and web actions use one resolver. Forgetting a harness note
  suppresses it only in muninn, with no upstream writes. A native imported copy
  is canonical, and forget retires it and suppresses every harness mirror.
- [x] Recall lines are `- [2026-11-02 k7m2qx] ...`, with shorter dated-note and
  correction instructions.

## Project memories on their own branch

`muninn sync` moves from `.muninn/memories.jsonl` on main to its own orphan
branch `muninn`, checked out as the worktree `.muninn/` (excluded from main via
`.git/info/exclude`). Tasks get their own `huginn` branch: separate things stay
separate (user, 2026-11-05 15:47). Migration: one commit removes the tracked
`.muninn/` from main, then the same file lands on the `muninn` branch. The
git-branch code is one shared package used by both tools. See
~/Projects/huginn/DESIGN.md D6.

## Compaction

Merge near-duplicate and superseded memories (newest wins) so the store and
recall stay small.

## Web UI from vendor apps

Both tools run a local web UI (`muninn serve`, `huginn serve`) that can be
opened from vendor apps (Claude, ChatGPT/Codex) to view and edit memories and
tasks. TUI first, web UI later.

## Maybe: hosted service

A small-fee hosted service to view, edit and sync without self-hosting. Its
point is avoiding lock-in to one company's tool. Separate design, later.
