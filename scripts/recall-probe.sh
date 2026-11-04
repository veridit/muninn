#!/usr/bin/env bash
# Recall probe for a throwaway home: prints what each message would inject and
# the wall time of one cold `muninn recall` process (what a hook pays).
set -euo pipefail
cd "$(dirname "$0")/.."
export MUNINN_HOME="${MUNINN_HOME:-/tmp/muninn-e2e}"
go build -o bin/muninn ./cmd/muninn
for q in "banana mode please" "bake a banana pie" "what indentation does this repo use" "write a bash for loop"; do
  printf '%-40s -> ' "$q"
  out=$(./bin/muninn recall "$q")
  if [ -n "$out" ]; then echo "$out" | tail -n +2 | tr '\n' ' ' | cut -c1-110; echo; else echo "(nothing)"; fi
done
start=$(python3 -c 'import time;print(time.time())')
./bin/muninn recall "banana mode" > /dev/null
python3 -c "import time;print('cold recall process: %.0f ms' % ((time.time()-$start)*1000))"
