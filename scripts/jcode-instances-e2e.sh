#!/usr/bin/env bash
# jcode multi-instance e2e in a throwaway home: two isolated instances,
# install into both (keeping an existing hook), then drive `muninn hook jcode`
# as each instance would and check that every injection stays in-instance.
set -euo pipefail
cd "$(dirname "$0")/.."
tmp="${TMPDIR:-/tmp}"
root=$(mktemp -d "${tmp%/}/muninn-instances.XXXXXX") # macOS TMPDIR ends in '/'
trap 'rm -rf "$root"' EXIT
export MUNINN_HOME="$root/muninn"
go build -o bin/muninn ./cmd/muninn
m=$PWD/bin/muninn

mkinst() { # name fact
  local h="$root/$1/.jcode"
  mkdir -p "$h/memory/projects" "$h/sessions" "$h/bin"
  printf '[hooks]\npost_tool_feedback = "echo keep-me"\n' > "$h/config.toml"
  cat > "$h/memory/projects/samehash.json" <<EOF
{"graph_version":1,"memories":{"m1":{"id":"m1","content":"$2","category":"fact","active":true}}}
EOF
  # Stub jcode: record what muninn would inject.
  printf '#!/bin/sh\nprintf "%%s\\n" "$JCODE_HOME" "$@" > "%s/injected"\n' "$h" > "$h/bin/jcode"
  chmod +x "$h/bin/jcode"
  cat > "$h/sessions/s1.json" <<EOF
{"id":"s1","messages":[{"role":"user","content":[{"type":"text","text":"how do we deploy the payment service"}]}]}
EOF
}
mkinst alpha "Alpha deploys the payment service with blue green rollout on Fridays"
mkinst beta "Beta deploys the payment service with a canary rollout on Mondays"

mkdir -p "$MUNINN_HOME/config"
printf '{"jcode_homes":["%s","%s"]}\n' "$root/alpha/.jcode" "$root/beta/.jcode" > "$MUNINN_HOME/config/config.json"
"$m" index >/dev/null

echo "== instances"
"$m" jcode instances
echo "== install into both (dry-run first, then real)"
"$m" install jcode --jcode-home "$root/alpha/.jcode" --jcode-home "$root/beta/.jcode" --dry-run
"$m" install jcode --jcode-home "$root/alpha/.jcode" --jcode-home "$root/beta/.jcode"
grep -q 'post_tool_feedback = "echo keep-me"' "$root/alpha/.jcode/config.toml" && echo "existing hook kept: ok"
"$m" install jcode --jcode-home "$root/alpha/.jcode" | grep -q 'already up to date' && echo "re-install idempotent: ok"

fail=0
echo "== shell CLI and unbound Claude hook"
got=$(JCODE_HOME="$root/alpha/.jcode" "$m" search "payment service rollout" --limit 10)
if grep -q "Alpha deploys" <<<"$got" && ! grep -q "Beta deploys" <<<"$got"; then
  echo "alpha cli: own memories only: ok"
else
  echo "alpha cli: FAIL"; echo "$got"; fail=1
fi
env -u JCODE_HOME "$m" remember --scope global "The payment service rollout always uses a written checklist" >/dev/null
got=$(printf '%s\n' '{"session_id":"claude-global","prompt":"payment service rollout"}' | env -u JCODE_HOME "$m" hook claude)
if grep -q "written checklist" <<<"$got" && ! grep -qE "Alpha deploys|Beta deploys" <<<"$got"; then
  echo "claude hook: global memories only, no instance text: ok"
else
  echo "claude hook: FAIL"; echo "$got"; fail=1
fi
for inst in alpha beta; do
  h="$root/$inst/.jcode"
  JCODE_HOME="$h" JCODE_HOOK_SESSION_ID=s1 "$m" hook jcode
  got=$(cat "$h/injected")
  other=$([ $inst = alpha ] && echo Beta || echo Alpha)
  # Require a short Crockford id, with an optional date.
  if grep -q "^$h$" <<<"$got" && grep -qiE "^\.0:([0-9]{4}-[0-9]{2}-[0-9]{2})? \.1:[0123456789abcdefghjkmnpqrstvwxyz]{6,} \.2:\`${inst} deploys" <<<"$got" && ! grep -q "$other deploys" <<<"$got"; then
    echo "$inst: injected own memory only: ok"
  else
    echo "$inst: FAIL"; echo "$got"; fail=1
  fi
done

echo "== shared project memories (.muninn/memories.jsonl)"
repo="$root/alpha/app"
mkdir -p "$repo" && git -C "$repo" init -q && git -C "$repo" -c user.email=t@example.invalid -c user.name=t commit -q --allow-empty -m root
(cd "$repo" && "$m" remember --project "The payment service release checklist lives in docs/RELEASE.md" >/dev/null && "$m" sync >/dev/null)
test -s "$repo/.muninn/memories.jsonl" && echo "project file written: ok"
cat > "$root/alpha/.jcode/sessions/s2.json" <<EOF
{"id":"s2","messages":[{"role":"user","content":[{"type":"text","text":"where is the payment service release checklist"}]}]}
EOF
JCODE_HOME="$root/alpha/.jcode" JCODE_HOOK_SESSION_ID=s2 JCODE_HOOK_CWD="$repo" "$m" hook jcode
if grep -q "docs/RELEASE.md" "$root/alpha/.jcode/injected"; then echo "alpha in repo: project memory recalled: ok"; else echo "alpha in repo: FAIL"; fail=1; fi
cp "$root/alpha/.jcode/sessions/s2.json" "$root/beta/.jcode/sessions/s2.json"
rm -f "$root/beta/.jcode/injected"
JCODE_HOME="$root/beta/.jcode" JCODE_HOOK_SESSION_ID=s2 "$m" hook jcode
if ! grep -qs "docs/RELEASE.md" "$root/beta/.jcode/injected"; then echo "beta outside repo: project memory not recalled: ok"; else echo "beta: FAIL (leak)"; fail=1; fi

echo "== MCP server as installed in mcp.json (no JCODE_HOME in env)"
for inst in alpha beta; do
  other=$([ $inst = alpha ] && echo Beta || echo Alpha)
  got=$(env -u JCODE_HOME python3 - "$root/$inst/.jcode/mcp.json" <<'PY'
import json, subprocess, sys
srv = json.load(open(sys.argv[1]))["servers"]["muninn"]
p = subprocess.Popen([srv["command"], *srv["args"]], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True)
def rpc(i, method, params):
    p.stdin.write(json.dumps({"jsonrpc": "2.0", "id": i, "method": method, "params": params}) + "\n"); p.stdin.flush()
    while True:
        m = json.loads(p.stdout.readline())
        if m.get("id") == i: return m
rpc(1, "initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "e2e", "version": "0"}})
p.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n"); p.stdin.flush()
r = rpc(2, "tools/call", {"name": "memory_search", "arguments": {"query": "payment service rollout", "limit": 10}})
print(r["result"]["content"][0]["text"]); p.stdin.close(); p.wait()
PY
)
  if grep -qi "\"${inst} deploys" <<<"$got" && ! grep -q "$other deploys" <<<"$got"; then
    echo "$inst mcp: own memories only: ok"
  else
    echo "$inst mcp: FAIL"; echo "$got"; fail=1
  fi
done
echo "== capture fake-provider acceptance"
python3 scripts/capture-e2e.py "$m" "$root"
exit $fail
