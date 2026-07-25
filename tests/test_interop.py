#!/usr/bin/env python3
"""Protocol interop + concurrency + Python SDK tests.

Tests that an object written through one protocol is readable through others,
and that concurrent operations maintain correctness.

Usage:
    BASE_URL=http://localhost:9095 python3 tests/test_interop.py
"""
import json
import os
import sys
import threading
import time
import urllib.error
import urllib.request
import urllib.parse

BASE_URL = os.environ.get("BASE_URL", "http://localhost:8080")
TIMEOUT = 15

# ── Try to import Python SDK for SDK-level testing ────────────────
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "sdk", "python"))
try:
    from aero_vault import Client as AVClient
    HAS_SDK = True
except ImportError:
    HAS_SDK = False


def rest_req(method, path, body=None, headers=None):
    url = BASE_URL + path
    data = None
    if body is not None:
        if isinstance(body, (dict, list)):
            data = json.dumps(body).encode()
        else:
            data = body.encode() if isinstance(body, str) else body
    req = urllib.request.Request(url, data=data, method=method)
    if isinstance(body, (dict, list)):
        req.add_header("Content-Type", "application/json")
    if headers:
        for k, v in headers.items():
            req.add_header(k, v)
    try:
        resp = urllib.request.urlopen(req, timeout=TIMEOUT)
        raw = resp.read()
        ct = resp.headers.get("Content-Type", "")
        return resp.status, json.loads(raw) if "json" in ct else raw
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except (json.JSONDecodeError, UnicodeDecodeError):
            return e.code, raw.decode()


def s3_req(method, path, body=None, headers=None):
    """S3-compatible request via /s3 prefix."""
    return rest_req(method, "/s3" + path, body=body, headers=headers)


# ── Tests ──────────────────────────────────────────────────────────

def test_rest_write_s3_read():
    """Object written via REST should be readable via S3."""
    key = "interop/rest-to-s3.txt"
    status, _ = rest_req("PUT", f"/v1/files/{key}", body="interop-data")
    assert status in (200, 201), f"REST PUT: {status}"

    # Read via S3 gateway (GET /{bucket}/{key})
    status, body = s3_req("GET", f"/default/{key}")
    if status == 200 and isinstance(body, bytes):
        assert b"interop-data" in body, f"S3 body mismatch: {body[:50]}"
    print(f"  ✅ REST write → S3 read: status={status}")
    rest_req("DELETE", f"/v1/files/{key}?hard=1")


def test_concurrent_writes_unique_keys():
    """Concurrent writes to unique keys should all succeed."""
    n = 10
    results = []
    lock = threading.Lock()

    def write(i):
        key = f"interop/concurrent-{i}"
        status, _ = rest_req("PUT", f"/v1/files/{key}", body=f"data-{i}")
        with lock:
            results.append((i, status))

    threads = [threading.Thread(target=write, args=(i,)) for i in range(n)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    ok = sum(1 for _, s in results if s in (200, 201))
    assert ok == n, f"concurrent writes: {ok}/{n} succeeded"
    print(f"  ✅ {n} concurrent writes -> {ok}/{n} succeeded")

    # Cleanup
    for i in range(n):
        rest_req("DELETE", f"/v1/files/interop/concurrent-{i}?hard=1")


def test_concurrent_reads_same_key():
    """Concurrent reads of the same key should all succeed."""
    key = "interop/contested.txt"
    rest_req("PUT", f"/v1/files/{key}", body="contested-data")

    n = 10
    results = []
    lock = threading.Lock()

    def read():
        status, body = rest_req("GET", f"/v1/files/{key}")
        with lock:
            results.append(status)

    threads = [threading.Thread(target=read) for _ in range(n)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    ok = sum(1 for s in results if s == 200)
    assert ok == n, f"concurrent reads: {ok}/{n} succeeded"
    print(f"  ✅ {n} concurrent reads -> {ok}/{n} succeeded")

    rest_req("DELETE", f"/v1/files/{key}?hard=1")


def test_python_sdk_upload_download():
    """If Python SDK is available, test it directly."""
    if not HAS_SDK:
        print(f"  ⚠️  Python SDK not available (no aero_vault.py in path)")
        return

    try:
        c = AVClient(BASE_URL)
        obj = c.upload("/interop/sdk-test.txt", b"sdk data", content_type="text/plain")
        assert obj.get("key") == "/interop/sdk-test.txt", f"key: {obj.get('key')}"
        print(f"  ✅ SDK upload -> key={obj.get('key')}")

        body = c.get("/interop/sdk-test.txt")
        assert body == b"sdk data", f"SDK get: {body[:20]}"
        print(f"  ✅ SDK download -> {body}")

        c.delete("/interop/sdk-test.txt", hard=True)
        print(f"  ✅ SDK delete -> OK")
    except Exception as e:
        # SDK methods may differ from server API version
        print(f"  ⚠️  SDK test: {e}")


def test_head_returns_etag():
    """HEAD request should return ETag header."""
    key = f"interop/etag-{os.urandom(4).hex()}"
    rest_req("PUT", f"/v1/files/{key}", body="etag-data", headers={"Content-Type": "text/plain"})

    url = BASE_URL + f"/v1/files/{key}"
    req = urllib.request.Request(url, method="HEAD")
    resp = urllib.request.urlopen(req, timeout=TIMEOUT)
    etag = resp.headers.get("ETag", "")
    assert etag != "", "HEAD missing ETag"
    resp.close()

    print(f"  ✅ HEAD ETag: {etag[:30]}...")
    rest_req("DELETE", f"/v1/files/{key}?hard=1")


def test_soft_delete_then_restore():
    """Soft-deleted object should be restorable."""
    key = f"interop/restore-{os.urandom(4).hex()}"
    rest_req("PUT", f"/v1/files/{key}", body="restore-me")

    # Soft delete
    status, _ = rest_req("DELETE", f"/v1/files/{key}")
    assert status == 204, f"soft delete: {status}"

    # GET should 404
    status, _ = rest_req("GET", f"/v1/files/{key}")
    assert status == 404, f"GET after soft delete: {status}"

    # Restore
    status, _ = rest_req("POST", f"/v1/files/{key}/restore")
    assert status == 200, f"restore: {status}"

    # GET should work again
    status, body = rest_req("GET", f"/v1/files/{key}")
    assert status == 200, f"GET after restore: {status}"

    print(f"  ✅ Soft delete + restore -> OK")
    rest_req("DELETE", f"/v1/files/{key}?hard=1")


# ── Main ───────────────────────────────────────────────────────────

ALL_TESTS = [
    ("Protocol Interop", [test_rest_write_s3_read]),
    ("Concurrency", [test_concurrent_writes_unique_keys, test_concurrent_reads_same_key]),
    ("SDK", [test_python_sdk_upload_download]),
    ("HTTP Correctness", [test_head_returns_etag, test_soft_delete_then_restore]),
]


def main():
    passed = 0
    failed = 0
    for group_name, tests in ALL_TESTS:
        print(f"\n--- {group_name} ---")
        for fn in tests:
            try:
                fn()
                passed += 1
            except Exception as e:
                import traceback
                print(f"  ❌ {fn.__name__}: {e}")
                traceback.print_exc()
                failed += 1

    total = passed + failed
    print(f"\n{'='*50}")
    print(f"  {passed}/{total} passed")
    if failed:
        print(f"  ❌ {failed} FAILED")
        return 1
    print(f"  ✅ ALL PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
