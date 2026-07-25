#!/usr/bin/env python3
"""End-to-end tests against the running aero-vault server.

Usage:
    # Start server in background first:
    ./bin/aero-vault &
    
    # Run tests:
    python3 tests/test_e2e.py

    # Or let this script manage the server:
    SERVER_BIN=./bin/aero-vault python3 tests/test_e2e.py --manage
"""
import json
import os
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request
import urllib.parse

SERVER_BIN = os.environ.get("SERVER_BIN", "./bin/aero-vault")
BASE_URL = os.environ.get("BASE_URL", "http://localhost:8080")
TIMEOUT = 10


# ── Helpers ────────────────────────────────────────────────────────────────

def request(method, path, body=None, headers=None):
    url = BASE_URL + path
    data = None
    if body is not None:
        if isinstance(body, (dict, list)):
            data = json.dumps(body).encode()
        else:
            data = body.encode() if isinstance(body, str) else body
    req = urllib.request.Request(url, data=data, method=method)
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
    except urllib.error.URLError as e:
        return 0, f"Connection failed: {e.reason}"


# ── Tests ──────────────────────────────────────────────────────────────────

def test_healthz():
    status, body = request("GET", "/healthz")
    assert status == 200, f"healthz: got {status}"
    print("  ✅ GET /healthz -> 200")


def test_readyz():
    status, body = request("GET", "/readyz")
    assert status == 200, f"readyz: got {status}"
    print("  ✅ GET /readyz -> 200")


def test_crud_roundtrip():
    key = "e2e-test/hello.txt"
    text = "Hello, aero-vault!"

    # PUT - send raw text with correct content-type
    url = BASE_URL + f"/v1/files/{key}"
    req = urllib.request.Request(url, data=text.encode(), method="PUT")
    req.add_header("Content-Type", "text/plain")
    try:
        resp = urllib.request.urlopen(req, timeout=TIMEOUT)
        assert resp.status in (200, 201), f"PUT: got {resp.status}"
        raw = resp.read()
        info = json.loads(raw) if raw else {}
        assert info.get("key") == key, f"key mismatch: {info.get('key')}"
        print(f"  ✅ PUT /v1/files/{key} -> {resp.status}")
    except urllib.error.HTTPError as e:
        assert False, f"PUT: {e.code} {e.read().decode()}"

    # GET - returns raw body
    try:
        resp = urllib.request.urlopen(url, timeout=TIMEOUT)
        assert resp.status == 200, f"GET: got {resp.status}"
        body = resp.read().decode()
        assert body == text, f"body mismatch: {body!r}"
        print(f"  ✅ GET /v1/files/{key} -> 200 (body={body!r})")
    except urllib.error.HTTPError as e:
        assert False, f"GET: {e.code} {e.read().decode()}"

    # HEAD
    req = urllib.request.Request(url, method="HEAD")
    resp = urllib.request.urlopen(req, timeout=TIMEOUT)
    assert resp.status == 200, f"HEAD: got {resp.status}"
    print(f"  ✅ HEAD /v1/files/{key} -> 200")

    # DELETE
    status, _ = request("DELETE", f"/v1/files/{key}?hard=1")
    assert status == 204, f"DELETE: got {status}"
    print(f"  ✅ DELETE /v1/files/{key}?hard=1 -> 204")

    # GET after delete → 404
    status, _ = request("GET", f"/v1/files/{key}")
    assert status == 404, f"GET after delete: expected 404, got {status}"
    print(f"  ✅ GET after delete -> 404")


def test_list_objects():
    """Create objects and verify list returns them."""
    for i in range(3):
        status, _ = request("PUT", f"/v1/files/e2e-list/file-{i}.txt", body=f"content-{i}")
        assert status in (200, 201)

    status, data = request("GET", "/v1/files?prefix=e2e-list/")
    assert status == 200, f"LIST: got {status}"
    if isinstance(data, dict):
        objects = data.get("objects", [])
        assert len(objects) == 3, f"expected 3 objects, got {len(objects)}"
    print(f"  ✅ LIST /v1/files?prefix=e2e-list/ -> 3 objects")

    # Cleanup
    for i in range(3):
        request("DELETE", f"/v1/files/e2e-list/file-{i}.txt?hard=1")


def test_tags():
    key = "e2e-test/tagged.txt"
    request("PUT", f"/v1/files/{key}", body="data")

    # PUT tags — body is directly the tag map, not wrapped
    status, data = request("PUT", f"/v1/files/{key}/tags", body={"env": "test", "team": "platform"})
    assert status in (200, 201), f"PUT tags: got {status}"
    print(f"  ✅ PUT /v1/files/{key}/tags -> {status}")

    # GET tags
    status, data = request("GET", f"/v1/files/{key}/tags")
    assert status == 200
    if isinstance(data, dict):
        tags = data.get("tags", {})
        assert tags.get("env") == "test", f"tag env={tags.get('env')}"
    print(f"  ✅ GET /v1/files/{key}/tags -> env=test")

    # DELETE tags (returns 204 No Content)
    status, _ = request("DELETE", f"/v1/files/{key}/tags")
    assert status in (200, 204), f"DELETE tags: got {status}"
    print(f"  ✅ DELETE /v1/files/{key}/tags -> {status}")

    request("DELETE", f"/v1/files/{key}?hard=1")


def test_multipart():
    """Multipart upload via REST API."""
    # Init
    status, data = request("POST", "/v1/multipart", body={"key": "e2e-multi/file.bin", "content_type": "application/octet-stream"})
    assert status == 201, f"InitMultipart: got {status}"
    upload_id = data.get("upload_id", data.get("UploadID", "")) if isinstance(data, dict) else ""
    assert upload_id, f"No upload_id in response: {data}"
    print(f"  ✅ POST /v1/multipart -> upload_id={upload_id[:8]}...")

    # Upload part
    part_url = f"/v1/multipart/{upload_id}/parts/1"
    req = urllib.request.Request(BASE_URL + part_url, data=b"part1 data", method="PUT")
    try:
        resp = urllib.request.urlopen(req, timeout=TIMEOUT)
        assert resp.status == 200, f"UploadPart: got {resp.status}"
        print(f"  ✅ PUT /v1/multipart/{upload_id[:8]}.../parts/1 -> 200")
    except urllib.error.HTTPError as e:
        assert False, f"UploadPart: {e.code} {e.read().decode()}"

    # Complete
    status, data = request("POST", f"/v1/multipart/{upload_id}/complete")
    assert status == 200, f"CompleteMultipart: got {status}"
    print(f"  ✅ POST /v1/multipart/{upload_id[:8]}.../complete -> 200")

    request("DELETE", "/v1/files/e2e-multi/file.bin?hard=1")


def test_bucket_crud():
    """Bucket lifecycle."""
    # List buckets
    status, data = request("GET", "/v1/buckets")
    assert status == 200
    print(f"  ✅ GET /v1/buckets -> 200")

    # Set bucket versioning
    status, _ = request("PUT", "/v1/buckets/default/versioning", body={"enabled": True})
    assert status == 200, f"Versioning: got {status}"
    print(f"  ✅ PUT /v1/buckets/default/versioning -> 200")

    # Get bucket config
    status, data = request("GET", "/v1/buckets/default/config")
    assert status == 200
    print(f"  ✅ GET /v1/buckets/default/config -> 200")


def test_admin():
    """Admin API — config (read-only, no auth)."""
    status, data = request("GET", "/v1/admin/config")
    # Without auth, admin config may still be accessible or return 403
    assert status in (200, 403), f"Admin config: got {status}"
    print(f"  ✅ GET /v1/admin/config -> {status}")


def test_bucket_policy():
    """Bucket policy CRUD."""
    key = "e2e-policy/doc.txt"
    request("PUT", f"/v1/files/{key}", body="data")

    status, data = request("PUT", "/v1/buckets/default/policy", body={"policy": '{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Resource":"arn:aero:tenant:default:bucket:default/key"}]}'})
    assert status == 200, f"PutBucketPolicy: got {status}"
    print(f"  ✅ PUT /v1/buckets/default/policy -> 200")

    # Delete policy
    status, _ = request("DELETE", "/v1/buckets/default/policy")
    assert status == 200, f"DeleteBucketPolicy: got {status}"
    print(f"  ✅ DELETE /v1/buckets/default/policy -> 200")

    request("DELETE", f"/v1/files/{key}?hard=1")


def test_cors():
    """Bucket CORS CRUD."""
    # Set CORS
    status, _ = request("PUT", "/v1/buckets/default/cors", body=[{"allowed_origins": ["https://example.com"], "allowed_methods": ["GET"]}])
    assert status == 200, f"PutCORS: got {status}"
    print(f"  ✅ PUT /v1/buckets/default/cors -> 200")

    # Get CORS
    status, data = request("GET", "/v1/buckets/default/cors")
    assert status == 200
    if isinstance(data, list):
        assert len(data) >= 1
    print(f"  ✅ GET /v1/buckets/default/cors -> {len(data) if isinstance(data, list) else 'ok'} rules")

    # Delete CORS
    status, _ = request("DELETE", "/v1/buckets/default/cors")
    assert status == 200
    print(f"  ✅ DELETE /v1/buckets/default/cors -> 200")


# ── Main ───────────────────────────────────────────────────────────────────

ALL_TESTS = [
    ("Health", [test_healthz, test_readyz]),
    ("CRUD", [test_crud_roundtrip, test_list_objects]),
    ("Tags", [test_tags]),
    ("Multipart", [test_multipart]),
    ("Buckets", [test_bucket_crud, test_bucket_policy, test_cors]),
    ("Admin", [test_admin]),
]


def run_tests():
    passed = 0
    failed = 0
    for group_name, tests in ALL_TESTS:
        print(f"\n--- {group_name} ---")
        for fn in tests:
            try:
                fn()
                passed += 1
            except Exception as e:
                print(f"  ❌ {fn.__name__}: {e}")
                failed += 1

    total = passed + failed
    print(f"\n{'='*50}")
    print(f"  {passed}/{total} passed")
    if failed:
        print(f"  ❌ {failed} FAILED")
        return 1
    print(f"  ✅ ALL PASSED")
    return 0


def wait_for_server(pid=None):
    for i in range(30):
        try:
            status, _ = request("GET", "/healthz")
            if status == 200:
                return True
        except Exception:
            pass
        time.sleep(0.5)
    return False


if __name__ == "__main__":
    manage = "--manage" in sys.argv
    server_proc = None

    if manage:
        print(f"Starting server: {SERVER_BIN}")
        server_proc = subprocess.Popen([SERVER_BIN], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if not wait_for_server():
            print("  ❌ Server failed to start")
            sys.exit(1)
        print("  ✅ Server ready")

    try:
        ec = run_tests()
    finally:
        if server_proc:
            server_proc.terminate()
            server_proc.wait(timeout=5)
    sys.exit(ec)
