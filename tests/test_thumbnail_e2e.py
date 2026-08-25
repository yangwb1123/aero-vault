#!/usr/bin/env python3
"""Thumbnail route smoke tests against a running aero-vault server."""
import base64
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

SERVER_BIN = os.environ.get("SERVER_BIN", "./bin/aero-vault")
BASE_URL = os.environ.get("BASE_URL", "http://localhost:8080")
TIMEOUT = 10
API_KEY = os.environ.get("E2E_API_KEY", "")
PNG_BYTES = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAG0lEQVR4nGL6z8DA8J+BiZHh/38GBkAAAAD//x0hBAKGdL17AAAAAElFTkSuQmCC"
)


def request(method, path, body=None, headers=None):
    req = urllib.request.Request(BASE_URL + path, data=body, method=method)
    merged_headers = dict(headers or {})
    if API_KEY and "X-Api-Key" not in merged_headers:
        merged_headers["X-Api-Key"] = API_KEY
    for key, value in merged_headers.items():
        req.add_header(key, value)
    try:
        resp = urllib.request.urlopen(req, timeout=TIMEOUT)
        return resp.status, resp.read(), resp.headers
    except urllib.error.HTTPError as err:
        return err.code, err.read(), err.headers
    except urllib.error.URLError:
        return 0, b"", {}



def test_thumbnail_smoke():
    key = "e2e-thumb/smoke.png"
    thumb_path = f"/v1/files/{key}/thumbnail?w=32&h=32"
    try:
        status, _, _ = request("PUT", f"/v1/files/{key}", PNG_BYTES, {"Content-Type": "image/png"})
        assert status in (200, 201), f"PUT source: got {status}"
        print(f"  ✅ PUT /v1/files/{key} -> {status}")

        status, body, headers = request("GET", thumb_path)
        assert status == 200, f"GET thumbnail: got {status}"
        assert body, "GET thumbnail returned empty body"
        assert headers.get("Content-Type", "").startswith("image/jpeg"), headers.get("Content-Type")
        etag = headers.get("ETag", "")
        assert etag, "GET thumbnail missing ETag"
        assert headers.get("Last-Modified", ""), "GET thumbnail missing Last-Modified"
        print(f"  ✅ GET {thumb_path} -> 200 (etag={etag})")

        status, body, headers = request("GET", thumb_path, headers={"If-None-Match": etag})
        assert status == 304, f"GET revalidation: got {status}"
        assert body == b"", f"GET 304 returned body length {len(body)}"
        print(f"  ✅ GET {thumb_path} If-None-Match -> 304")

        status, body, headers = request("HEAD", thumb_path, headers={"If-None-Match": etag})
        assert status == 304, f"HEAD revalidation: got {status}"
        assert body == b"", f"HEAD 304 returned body length {len(body)}"
        print(f"  ✅ HEAD {thumb_path} If-None-Match -> 304")
    finally:
        request("DELETE", f"/v1/files/{key}?hard=1")


ALL_TESTS = [("Thumbnail", [test_thumbnail_smoke])]


def run_tests():
    passed = 0
    failed = 0
    for group_name, tests in ALL_TESTS:
        print(f"\n--- {group_name} ---")
        for fn in tests:
            try:
                fn()
                passed += 1
            except Exception as err:
                print(f"  ❌ {fn.__name__}: {err}")
                failed += 1
    total = passed + failed
    print(f"\n{'=' * 50}")
    print(f"  {passed}/{total} passed")
    if failed:
        print(f"  ❌ {failed} FAILED")
        return 1
    print("  ✅ ALL PASSED")
    return 0



def wait_for_server():
    for _ in range(30):
        status, _, _ = request("GET", "/healthz")
        if status == 200:
            return True
        time.sleep(0.5)
    return False


if __name__ == "__main__":
    manage = "--manage" in sys.argv
    server_proc = None
    if manage:
        port = BASE_URL.split(":")[-1] if ":" in BASE_URL else "8080"
        env = os.environ.copy()
        env["APP_ADDR"] = f":{port}"
        if not API_KEY:
            env["AUTH_KEYS"] = "thumb-e2e:default:read+write"
            API_KEY = "thumb-e2e"
        print(f"Starting server: {SERVER_BIN}")
        server_proc = subprocess.Popen([SERVER_BIN], env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if not wait_for_server():
            print("  ❌ Server failed to start")
            server_proc.kill()
            sys.exit(1)
        print("  ✅ Server ready")
    try:
        sys.exit(run_tests())
    finally:
        if server_proc:
            server_proc.terminate()
            server_proc.wait(timeout=5)
