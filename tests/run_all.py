#!/usr/bin/env python3
"""Run all E2E test suites against a server.

Usage:
    # Against an already running server:
    python3 tests/run_all.py

    # Start server automatically (requires binary path):
    SERVER_BIN=./bin/aero-vault python3 tests/run_all.py --manage
"""
import os
import subprocess
import sys
import time
import urllib.request

BASE_URL = os.environ.get("BASE_URL", "http://localhost:8080")
SERVER_BIN = os.environ.get("SERVER_BIN", "./bin/aero-vault")
SUITES = [
    ("E2E", "test_e2e.py"),
    ("Adversarial", "test_adversarial.py"),
    ("Interop", "test_interop.py"),
    ("S3", "test_s3.py"),
]


def wait_for_server(timeout=15):
    for i in range(timeout * 2):
        try:
            resp = urllib.request.urlopen(BASE_URL + "/healthz", timeout=2)
            if resp.status == 200:
                return True
        except Exception:
            pass
        time.sleep(0.5)
    return False


def main():
    manage = "--manage" in sys.argv
    server_proc = None

    if manage:
        if not os.path.exists(SERVER_BIN):
            print(f"Server binary not found: {SERVER_BIN}")
            return 1
        port = BASE_URL.split(":")[-1] if ":" in BASE_URL else "8080"
        env = os.environ.copy()
        env["APP_ADDR"] = f":{port}"
        server_proc = subprocess.Popen([SERVER_BIN], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if not wait_for_server():
            print("Server failed to start")
            server_proc.kill()
            return 1

    tests_dir = os.path.dirname(os.path.abspath(__file__))
    total = passed = failed = 0

    for name, file in SUITES:
        path = os.path.join(tests_dir, file)
        result = subprocess.run([sys.executable, path], capture_output=True, text=True)
        output = result.stdout
        # Count test results from output
        for line in output.split("\n"):
            if "✅" in line or "❌" in line:
                if "✅" in line:
                    passed += 1
                else:
                    failed += 1
                total += 1

        # Print suite results
        last_lines = [l for l in output.split("\n") if l.strip()][-5:]
        for l in last_lines:
            if "/" in l and "passed" in l:
                print(f"  {name}: {l.strip()}")
                break
        else:
            print(f"  {name}: exit={result.returncode}")

    if server_proc:
        server_proc.terminate()
        server_proc.wait(timeout=5)

    print(f"\n  Total: {total} tests, {passed} passed, {failed} failed")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
