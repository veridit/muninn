#!/usr/bin/env python3
"""Drive `muninn tui` in a real pseudo-terminal: type a query, read the screen,
toggle explain, switch to recall mode, quit. Throwaway home, synthetic data.
Usage: scripts/tui-pty.py bin/muninn [home]"""
import os, pty, re, select, subprocess, sys, time

binary = sys.argv[1]
home = sys.argv[2] if len(sys.argv) > 2 else "/tmp/muninn-e2e"
env = dict(os.environ, MUNINN_HOME=home, TERM="xterm-256color", COLUMNS="120", LINES="30")
master, slave = pty.openpty()
import fcntl, struct, termios
fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack("HHHH", 30, 120, 0, 0))
p = subprocess.Popen([binary, "tui"], stdin=slave, stdout=slave, stderr=slave, env=env, close_fds=True)
os.close(slave)
buf = b""

def read_for(sec):
    global buf
    end = time.time() + sec
    while time.time() < end:
        r, _, _ = select.select([master], [], [], 0.05)
        if r:
            try:
                buf += os.read(master, 65536)
            except OSError:
                return

def screen():
    return re.sub(rb"\x1b\[[0-9;?]*[A-Za-z]|\x1b[()][A-Z0-9]|\x1b[=>]", b"", buf).decode("utf-8", "replace")

def send(s, wait=0.8):
    os.write(master, s.encode())
    read_for(wait)

checks = []
read_for(1.5)
checks.append(("prompt rendered", "search memories" in screen()))
send("banana pie", 1.5)
checks.append(("result rendered", "Bake banana cake" in screen()))
checks.append(("status line", "results in" in screen()))
send("\x05")  # ctrl+e
checks.append(("explain shown", "keyword=" in screen()))
send("\t", 1.2)  # tab -> recall preview
checks.append(("recall mode", "recall preview" in screen()))
send("\x1b", 0.8)  # esc
try:
    rc = p.wait(timeout=3)
except subprocess.TimeoutExpired:
    p.kill(); rc = None
checks.append(("exits on esc", rc == 0))
for name, ok in checks:
    print(("PASS " if ok else "FAIL ") + name)
sys.exit(0 if all(ok for _, ok in checks) else 1)
