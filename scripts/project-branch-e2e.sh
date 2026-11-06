#!/usr/bin/env bash
# Real CLI, two clones and three isolated local stores. Only synthetic text.
set -euo pipefail
m="$1"
tmp="${TMPDIR:-/tmp}"
root=$(mktemp -d "${tmp%/}/muninn-branch-e2e.XXXXXX")
trap 'rm -rf "$root"' EXIT
export MUNINN_NO_EMBED=1
export GIT_AUTHOR_NAME=synthetic GIT_COMMITTER_NAME=synthetic
export GIT_AUTHOR_EMAIL=synthetic@example.invalid GIT_COMMITTER_EMAIL=synthetic@example.invalid

git init -q --bare --initial-branch=main "$root/remote.git"
git init -q --initial-branch=main "$root/alice"
git -C "$root/alice" commit -q --allow-empty -m 'synthetic root'
git -C "$root/alice" remote add origin "$root/remote.git"
git -C "$root/alice" push -q -u origin main
git clone -q "$root/remote.git" "$root/bob"
head=$(git -C "$root/alice" rev-parse HEAD)
(cd "$root/alice" && MUNINN_HOME="$root/alice-store" "$m" remember --project --category decision 'Quartz releases require a synthetic checklist.' >/dev/null && MUNINN_HOME="$root/alice-store" "$m" sync)
(cd "$root/bob" && MUNINN_HOME="$root/bob-store" "$m" sync && MUNINN_HOME="$root/bob-store" "$m" remember --project 'Quartz artifacts use a synthetic signature.' >/dev/null && MUNINN_HOME="$root/bob-store" "$m" sync)
(cd "$root/alice" && MUNINN_HOME="$root/alice-store" "$m" sync)
for clone in alice bob; do
  test -z "$(git -C "$root/$clone" status --porcelain)"
  test "$(git -C "$root/$clone" rev-parse HEAD)" = "$head"
  test "$(git -C "$root/$clone/.muninn-notes" branch --show-current)" = muninn-notes
  grep -q 'synthetic checklist' "$root/$clone/.muninn-notes/memories.jsonl"
  grep -q 'synthetic signature' "$root/$clone/.muninn-notes/memories.jsonl"
done
echo 'PROJECT CLI branch: two clones exchanged memories, main HEAD unchanged, main git status clean: ok'

git init -q --initial-branch=main "$root/migrate"
mkdir -p "$root/migrate/.muninn"
printf 'synthetic-pinned-project\n' > "$root/migrate/.muninn/project"
printf '%s\n' '{"id":"legacy-note","content":"Legacy quartz records are synthetic.","category":"decision","active":true,"created":"2026-01-01T00:00:00Z","updated":"2026-01-01T00:00:00Z"}' > "$root/migrate/.muninn/memories.jsonl"
git -C "$root/migrate" add .muninn
git -C "$root/migrate" commit -q -m 'synthetic legacy'
head=$(git -C "$root/migrate" rev-parse HEAD)
(cd "$root/migrate" && MUNINN_HOME="$root/migrate-store" "$m" sync --migrate --dry-run)
test ! -e "$root/migrate/.muninn-notes"
test -f "$root/migrate/.muninn/memories.jsonl"
test "$(git -C "$root/migrate" rev-parse HEAD)" = "$head"
(cd "$root/migrate" && MUNINN_HOME="$root/migrate-store" "$m" sync --migrate)
test ! -e "$root/migrate/.muninn/memories.jsonl"
grep -q 'Legacy quartz records' "$root/migrate/.muninn-notes/memories.jsonl"
grep -q 'synthetic-pinned-project' "$root/migrate/.muninn/project"
test "$(git -C "$root/migrate" rev-list --count "$head..HEAD")" = 1
test -z "$(git -C "$root/migrate" status --porcelain)"
head=$(git -C "$root/migrate" rev-parse HEAD)
(cd "$root/migrate" && MUNINN_HOME="$root/migrate-store" "$m" sync --migrate)
test "$(git -C "$root/migrate" rev-parse HEAD)" = "$head"
echo 'PROJECT CLI migration: dry-run nonmutating, one main removal commit, marker preserved, repeat idempotent: ok'
