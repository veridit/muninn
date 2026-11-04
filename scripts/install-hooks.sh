#!/usr/bin/env bash
# Install the repo's git hooks (pre-commit secret scan).
set -euo pipefail
root=$(git rev-parse --show-toplevel)
hook="$root/.git/hooks/pre-commit"
cat > "$hook" <<'EOF'
#!/usr/bin/env bash
exec "$(git rev-parse --show-toplevel)/scripts/check-secrets.sh"
EOF
chmod +x "$hook"
echo "installed $hook"
