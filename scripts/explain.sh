#!/usr/bin/env bash
# Show ranked scores for queries in the throwaway home: scripts/explain.sh "q1" "q2" ...
set -euo pipefail
cd "$(dirname "$0")/.."
export MUNINN_HOME="${MUNINN_HOME:-/tmp/muninn-e2e}"
for q in "$@"; do echo "== $q"; ./bin/muninn search --explain -n 3 "$q"; done
