#!/usr/bin/env python3
"""Run all E2E test suites against a server."""
import json
import os
import subprocess
import sys
import time
import urllib.request

BASE_URL = os.environ.get("BASE_URL", "http://localhost:8080")
SERVER_BIN = os.environ.get("SERVER_BIN", "./bin/aero-vault")
SUITES = [
    ("E2E", "test_e2e.py"),
    ("Thumbnail", "test_thumbnail_e2e.py"),
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


def run_suite(name, path):
    result = subprocess.run([sys.executable, path], capture_output=True, text=True, timeout=120)
    lines = [l.strip() for l in result.stdout.split("\n") if l.strip()]
    summary = ""
    for l in lines:
        if "/" in l and "passed" in l:
            summary = l
            break
    if not summary:
        summary = f"exit={result.returncode}"
    return result.returncode == 0, summary


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

    # Also verify OpenAPI spec has >50 paths
    try:
        resp = urllib.request.urlopen(BASE_URL + "/openapi.json", timeout=5)
        spec = json.loads(resp.read())
        paths = len(spec.get("paths", {}))
    except Exception:
        paths = 0

    tests_dir = os.path.dirname(os.path.abspath(__file__))
    all_ok = True
    total_suites = 0
    for name, file in SUITES:
        path = os.path.join(tests_dir, file)
        ok, summary = run_suite(name, path)
        if not ok:
            all_ok = False
        total_suites += 1
        print(f"  {name}: {summary}")

    if server_proc:
        server_proc.terminate()
        server_proc.wait(timeout=5)

    print(f"  OpenAPI: {paths} paths")
    if paths < 50:
        print(f"  ⚠️  OpenAPI has only {paths} paths (expected 50+)")
        all_ok = False

    print(f"\n  {'✅ ALL PASSED' if all_ok else '❌ SOME FAILED'}")
    return 0 if all_ok else 1


if __name__ == "__main__":
    sys.exit(main())
