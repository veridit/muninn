#!/usr/bin/env python3
"""MCP stdio smoke test: initialize, list tools, call memory_search.
Usage: scripts/mcp-e2e.py bin/muninn [home]   (home defaults to /tmp/muninn-e2e)"""
import json, os, subprocess, sys

env = dict(os.environ, MUNINN_HOME=sys.argv[2] if len(sys.argv) > 2 else "/tmp/muninn-e2e")
p = subprocess.Popen([sys.argv[1], "mcp"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, env=env)

def rpc(i, method, params=None):
    msg = {"jsonrpc": "2.0", "id": i, "method": method}
    if params is not None:
        msg["params"] = params
    p.stdin.write(json.dumps(msg) + "\n"); p.stdin.flush()
    while True:
        line = p.stdout.readline()
        if not line:
            raise SystemExit("server closed")
        r = json.loads(line)
        if r.get("id") == i:
            return r

init = rpc(1, "initialize", {"protocolVersion": "2025-06-18", "capabilities": {},
                             "clientInfo": {"name": "e2e", "version": "0"}})
print("server:", init["result"]["serverInfo"])
p.stdin.write(json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n"); p.stdin.flush()
tools = rpc(2, "tools/list", {})["result"]["tools"]
print("tools:", sorted(t["name"] for t in tools))
res = rpc(3, "tools/call", {"name": "memory_search", "arguments": {"query": "banana pie", "limit": 1}})["result"]
print("memory_search top:", res["structuredContent"]["results"][0]["text"][:60])
res = rpc(4, "tools/call", {"name": "memory_recall", "arguments": {"message": "write a bash loop"}})["result"]
print("memory_recall (irrelevant):", res["content"][0]["text"])
p.stdin.close(); p.wait(timeout=5)
