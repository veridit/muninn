#!/usr/bin/env bash
# jcode multi-instance e2e in a throwaway home: two isolated instances,
# install into both (keeping an existing hook), then drive `muninn hook jcode`
# as each instance would and check that every injection stays in-instance.
set -euo pipefail
cd "$(dirname "$0")/.."
root=$(mktemp -d)
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
for inst in alpha beta; do
  h="$root/$inst/.jcode"
  JCODE_HOME="$h" JCODE_HOOK_SESSION_ID=s1 "$m" hook jcode
  got=$(cat "$h/injected")
  other=$([ $inst = alpha ] && echo Beta || echo Alpha)
  if grep -q "^$h$" <<<"$got" && grep -qi "^- ${inst}" <<<"$got" && ! grep -q "$other" <<<"$got"; then
    echo "$inst: injected own memory only: ok"
  else
    echo "$inst: FAIL"; echo "$got"; fail=1
  fi
done
exit $fail
