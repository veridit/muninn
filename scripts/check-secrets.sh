#!/usr/bin/env bash
# No-secrets gate. Runs before every commit (scripts/install-hooks.sh) and in CI.
# Scans the files that would be committed (staged by default, or every tracked
# file with --all) for credential shapes and for real-memory exports.
# Exit 1 on any hit. There is no bypass flag on purpose.
set -euo pipefail

mode="${1:-staged}"
if [ "$mode" = "--all" ]; then
  files=$(git ls-files)
else
  files=$(git diff --cached --name-only --diff-filter=ACMR)
fi
[ -n "$files" ] || exit 0

if command -v gitleaks >/dev/null 2>&1; then
  if [ "$mode" = "--all" ]; then
    gitleaks detect --no-banner --redact -v
  else
    gitleaks protect --staged --no-banner --redact -v
  fi
fi

# Shapes: OpenAI/Anthropic/OpenRouter style keys, GitHub tokens, AWS keys,
# Slack tokens, Google API keys, private key blocks, and KEY=value assignments
# with a non-placeholder value.
pattern='(sk-(proj-|ant-|or-)?[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{40,}|AKIA[0-9A-Z]{16}|xox[abprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{35}|-----BEGIN [A-Z ]*PRIVATE KEY-----|(API_KEY|SECRET|TOKEN|PASSWORD)[A-Z_]*=[^[:space:]<$"'"'"'{]{8,})'

hits=0
while IFS= read -r f; do
  [ -f "$f" ] || continue
  case "$f" in scripts/check-secrets.sh) continue ;; esac
  if grep -I -n -E "$pattern" "$f" >/dev/null 2>&1; then
    echo "secret-like content in $f:" >&2
    grep -I -n -E "$pattern" "$f" | sed -E 's/(.{0,12}).*/\1…  [redacted]/' >&2
    hits=1
  fi
  # Real memory stores must never be committed.
  case "$f" in
    *.db|*.sqlite|*.sqlite3|*/memory/global.json|*/memory/projects/*.json)
      echo "memory store must not be committed: $f" >&2; hits=1 ;;
  esac
done <<< "$files"

if [ "$hits" -ne 0 ]; then
  echo "check-secrets: refusing. Remove the content above (fixtures must be synthetic)." >&2
  exit 1
fi
