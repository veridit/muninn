# muninn roadmap

Decided by the user on 2026-11-05 13:36, not built yet.

## Short ids, like huginn tasks

Today an injected memory shows up as
`jcode:mem_1793616201092_18365359773263918457` (44 chars), and native ones as
`mem_...`. That is long, costs tokens in every recall, and leaks where a memory
was imported from.

- Every memory gets a 6-char Crockford base32 id (`k7m2qx`), the same scheme
  as huginn. Any unique prefix resolves.
- Where it came from (jcode id, engram id, file path) moves into a mapping
  table (`origins`: short id -> source, upstream id). It is shown only on
  `muninn show <id>` or `--explain`, never in recall output.
- `forget`, MCP and the web UI accept the short id and the old long ids.
- Recall lines become `- [2026-11-02 k7m2qx] ...`.

## Project memories on the shared branch

`muninn sync` moves from `.muninn/memories.jsonl` on main to `memories/` in the
`.huginn/` worktree branch, so memories sync with tasks and never add commits
to main. See ~/Projects/huginn/DESIGN.md D4.

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
