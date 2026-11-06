#!/usr/bin/env bash
# Install the shared pre-commit secret scanner, including orphan worktrees.
set -euo pipefail
hook=$(git rev-parse --path-format=absolute --git-path hooks/pre-commit)
mkdir -p "$(dirname "$hook")"
cat > "$hook" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
scanner="$(git rev-parse --show-toplevel)/scripts/check-secrets.sh"
if [ -f "$scanner" ]; then
  exec bash "$scanner"
fi
# Notes branches contain data only. Find a registered code checkout, but keep
# this worktree as cwd: the scanner must inspect the files actually committed.
while IFS= read -r line; do
  case "$line" in
    'worktree '*)
      scanner="${line#worktree }/scripts/check-secrets.sh"
      if [ -f "$scanner" ]; then
        exec bash "$scanner"
      fi
      ;;
  esac
done < <(git worktree list --porcelain)
echo 'muninn: secret scanner unavailable in registered code worktrees, refusing commit' >&2
exit 1
EOF
chmod +x "$hook"
echo "installed $hook"
