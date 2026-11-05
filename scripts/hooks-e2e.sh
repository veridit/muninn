#!/usr/bin/env bash
# Harness-protocol smoke test in the throwaway home:
#  - Claude Code / Codex UserPromptSubmit payload -> additionalContext JSON
#  - per-session dedupe (second identical prompt injects nothing)
#  - own-output loop guard
#  - web UI API on loopback
set -euo pipefail
cd "$(dirname "$0")/.."
export MUNINN_HOME="${MUNINN_HOME:-/tmp/muninn-e2e}"
go build -o bin/muninn ./cmd/muninn
payload='{"session_id":"s-123","prompt":"banana mode please","cwd":"/tmp","hook_event_name":"UserPromptSubmit"}'
echo "== claude hook, first prompt";  echo "$payload" | ./bin/muninn hook claude
echo "== claude hook, same session again (dedupe -> no output)"; echo "$payload" | ./bin/muninn hook claude; echo "[end]"
echo "== codex hook, own-output loop guard (no output)"
echo '{"session_id":"s-9","prompt":"[muninn recall] - banana mode","hook_event_name":"UserPromptSubmit"}' | ./bin/muninn hook codex; echo "[end]"
echo "== web API"
./bin/muninn serve --addr 127.0.0.1:17431 --no-watch > "$MUNINN_HOME/serve-e2e.log" 2>&1 &
pid=$!
trap 'kill $pid 2>/dev/null' EXIT
for _ in 1 2 3 4 5 6 7 8 9 10; do curl -s -o /dev/null http://127.0.0.1:17431/api/stats && break; sleep 0.3; done
curl -s 'http://127.0.0.1:17431/api/search?q=banana%20pie' | python3 -c "import json,sys;d=json.load(sys.stdin);print('search top:', d['results'][0]['text'][:60])"
curl -s -X POST -H 'Content-Type: application/json' -d '{"message":"4-space indentation"}' http://127.0.0.1:17431/api/recall | python3 -c "import json,sys;d=json.load(sys.stdin);print('recall block:', d['block'].splitlines()[1][:70], '|', d['elapsed'])"
printf 'index.html: '; curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:17431/
printf 'foreign Host header refused: '; curl -s -o /dev/null -w '%{http_code}\n' -H 'Host: evil.example' http://127.0.0.1:17431/api/stats
printf 'cross-origin POST refused: '; curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Origin: http://evil.example' -d '{}' http://127.0.0.1:17431/api/remember
