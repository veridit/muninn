#!/usr/bin/env bash
# Synthetic acceptance: the shared hook scans the notes cwd, never bypasses it.
set -euo pipefail
source_root=$(cd "$(dirname "$0")/.." && pwd)
root=$(mktemp -d "${TMPDIR%/}/muninn-secret-hooks.XXXXXX")
trap 'rm -rf "$root"' EXIT
export GIT_AUTHOR_NAME=synthetic GIT_COMMITTER_NAME=synthetic
export GIT_AUTHOR_EMAIL=synthetic@example.invalid GIT_COMMITTER_EMAIL=synthetic@example.invalid
git init -q --initial-branch=main "$root/repo"
mkdir -p "$root/repo/scripts"
cp "$source_root/scripts/check-secrets.sh" "$source_root/scripts/install-hooks.sh" "$root/repo/scripts/"
git -C "$root/repo" add scripts
git -C "$root/repo" commit -q -m 'synthetic code checkout'
git -C "$root/repo" worktree add -q --detach "$root/code" HEAD
(cd "$root/code" && bash scripts/install-hooks.sh >/dev/null)
git -C "$root/repo" worktree add -q --orphan -b muninn-notes "$root/notes"
printf '%s\n' '{"id":"synthetic","content":"Quartz release checklist."}' > "$root/notes/memories.jsonl"
git -C "$root/notes" add memories.jsonl
git -C "$root/notes" commit -q -m 'synthetic safe notes'
head=$(git -C "$root/notes" rev-parse HEAD)
printf 'sk-%030d\n' 0 > "$root/notes/credential.txt"
git -C "$root/notes" add credential.txt
if git -C "$root/notes" commit -q -m 'must be refused' > "$root/rejected.log" 2>&1; then
  echo 'secret-like content passed the orphan worktree hook' >&2
  exit 1
fi
test "$(git -C "$root/notes" rev-parse HEAD)" = "$head"
git -C "$root/notes" rm -q --cached credential.txt
rm "$root/notes/credential.txt"
# Losing the code checkouts must fail closed, not disable the gate.
rm "$root/repo/scripts/check-secrets.sh" "$root/code/scripts/check-secrets.sh"
printf '%s\n' 'Synthetic safe note.' >> "$root/notes/memories.jsonl"
git -C "$root/notes" add memories.jsonl
if git -C "$root/notes" commit -q -m 'missing scanner refused' > "$root/missing.log" 2>&1; then
  echo 'missing scanner did not fail closed' >&2
  exit 1
fi
grep -q 'secret scanner unavailable' "$root/missing.log"
echo 'SECRET HOOK: installed from worktree, safe orphan notes accepted, credential refused, missing scanner fails closed: ok'
