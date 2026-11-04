#!/usr/bin/env bash
# End-to-end smoke test against a real local Ollama, in a throwaway home.
# Usage: scripts/e2e.sh [home-dir]   (default /tmp/muninn-e2e). Synthetic data only.
set -euo pipefail
cd "$(dirname "$0")/.."
export MUNINN_HOME="${1:-/tmp/muninn-e2e}"
mkdir -p "$MUNINN_HOME/config"
[ -f "$MUNINN_HOME/config/config.json" ] || printf '{"jcode_memory_dir":"/nonexistent"}\n' > "$MUNINN_HOME/config/config.json"
go build -o bin/muninn ./cmd/muninn
m() { ./bin/muninn "$@"; }

m remember --category preference 'Trigger word "banana mode": when the user says banana mode, reply in Norwegian, at most 3 lines.'
m remember 'The project uses 4-space indentation in all source files.'
m remember 'Bake banana cake at 175 degrees for 50 minutes.'
m remember 'Deploys go through the staging environment before production.'
m remember 'Use ripgrep instead of grep for repository searches.'
echo "== index"; m index
m stats | grep -E '"(active|vectors|concepts|communities|neighbor_edges|embedder)"'
for q in "banana mode" "how long should I bake a banana pie" "what indentation does this repo use" "write a bash for loop"; do
  echo "== search: $q"; m search --explain -n 2 "$q"
done
echo "== recall (thresholded, what a harness would get)"
for q in "banana mode please" "bake a banana pie" "write a bash for loop"; do
  printf '%s -> ' "$q"; out=$(m recall "$q"); [ -n "$out" ] && echo "$out" | tail -n +2 | head -2 || echo "(nothing)"
done
