#!/usr/bin/env python3
"""Synthetic capture acceptance against a loopback OpenAI-compatible server."""
import http.server
import json
import os
import pathlib
import subprocess
import sys
import threading

binary, root = sys.argv[1], pathlib.Path(sys.argv[2])
config = root / "muninn/config/config.json"
seen = []

class Provider(http.server.BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        seen.append(request)
        text = json.loads(request["messages"][1]["content"])["transcript"]
        label = "Client" if "Client capture" in text else "Alpha" if "Alpha capture" in text else "Beta"
        candidate = {"category": "decision", "trust": "high", "content": f"{label} capture uses a quartz release protocol."}
        body = json.dumps({"choices": [{"message": {"content": json.dumps(candidate)}, "finish_reason": "stop"}], "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Provider)
thread = threading.Thread(target=server.serve_forever, daemon=True)
thread.start()
cfg = json.loads(config.read_text())
cfg["extract"] = {"provider": "ollama", "base_url": f"http://127.0.0.1:{server.server_port}/v1", "model": "synthetic-chat", "every_turns": 1, "timeout_seconds": 2, "audit_days": 30}
config.write_text(json.dumps(cfg))

def command(args, home=None, **extra):
    env = dict(os.environ)
    if home is not None:
        env["JCODE_HOME"] = str(home)
    env.update(extra)
    result = subprocess.run([binary, *args], env=env, text=True, capture_output=True, timeout=15, cwd=root)
    assert result.returncode == 0, (args, result.returncode, result.stdout, result.stderr)
    return result

def session(home, identity, cwd, label):
    (home / "sessions" / f"{identity}.json").write_text(json.dumps({"working_dir": str(cwd), "messages": [{"id": "message-one", "role": "user", "content": [{"type": "text", "text": f"{label} capture prefers a quartz release protocol."}]}]}))

def hook(home, identity):
    output = command(["capture", "--hook"], home, JCODE_HOOK_EVENT="session_end", JCODE_HOOK_SESSION_ID=identity)
    assert not output.stdout and not output.stderr, output

for inst in ("alpha", "beta"):
    home = root / inst / ".jcode"
    session(home, "capture-one", root / inst, inst.title())
    hook(home, "capture-one")
    runs = json.loads(command(["capture", "log", "--session", "capture-one", "--json"], home).stdout)
    run = next(r for r in runs if r["home"] == str(home))
    assert run["scope"].startswith("jcode@"), run
    assert run["trigger"] == "session_end" and run["report"]["processed"] == 1, run
    assert run["report"]["actions"][0]["category"] == "decision", run
    assert run["calls"][0]["usage"]["total_tokens"] == 15, run
    own = command(["search", "quartz release protocol", "--limit", "20"], home).stdout
    other = "Beta" if inst == "alpha" else "Alpha"
    assert f"{inst.title()} capture uses" in own and f"{other} capture uses" not in own, own
    print(f"{inst} capture: instance scope, audit request/actions/tokens, no cross-instance recall: ok")

client = root / "client"
(client / ".muninn").mkdir(parents=True)
(client / ".muninn/client").write_text("synthetic-client\n")
home = root / "alpha/.jcode"
session(home, "capture-client", client, "Client")
hook(home, "capture-client")
runs = json.loads(command(["capture", "log", "--session", "capture-client", "--json"]).stdout)
assert runs[0]["scope"] == "client:synthetic-client", runs
request = json.loads(runs[0]["calls"][0]["request"]["messages"][1]["content"])
assert all("Alpha" not in m["content"] and "Beta" not in m["content"] for m in request["known"] or []), request
assert "Client capture uses" not in command(["search", "quartz release protocol", "--all"], home).stdout
print("client capture: persisted cwd overrides shell, known-memory context and owner --all stay strict: ok")

# Replaying a completed final hook makes no second provider request.
before = len(seen)
hook(home, "capture-client")
assert len(seen) == before
print("capture observer replay: no duplicate provider call: ok")

server.shutdown()
server.server_close()
thread.join()
for name, provider in (("no-key", "openai-compatible"), ("network-down", "ollama")):
    cfg["extract"]["provider"] = provider
    cfg["extract"]["api_key_env"] = "MUNINN_SYNTHETIC_MISSING_CREDENTIAL"
    config.write_text(json.dumps(cfg))
    session(home, name, root / "alpha", "Alpha")
    log = root / "muninn/data/hook.log"
    before = len(log.read_text().splitlines())
    hook(home, name)
    lines = log.read_text().splitlines()
    assert len(lines) == before + 1 and "capture failed (details withheld)" in lines[-1], lines
    runs = json.loads(command(["capture", "log", "--session", name, "--json"]).stdout)
    assert len(runs) == 1 and runs[0]["error"], runs
    print(f"capture {name}: exit=0, silent stdout/stderr, exactly one safe log line, error audited: ok")
