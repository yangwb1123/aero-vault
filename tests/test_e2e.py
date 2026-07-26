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

    # Set lifecycle with transitions
    status, _ = request("PUT", "/v1/buckets/default/lifecycle", body={
        "days": 365,
        "action": "soft_delete",
        "transition_rules": [
            {"days": 30, "storage_class": "STANDARD_IA"},
            {"days": 90, "storage_class": "GLACIER"},
        ],
    })
    assert status == 200, f"lifecycle with transitions: {status}"
    print(f"  ✅ PUT /v1/buckets/default/lifecycle (with transitions) -> 200")

    # Read lifecycle back
    status, data = request("GET", "/v1/buckets/default/lifecycle")
    assert status == 200
    assert len(data.get("transition_rules", [])) == 2, f"expected 2 transition rules, got {data}"
    print(f"  ✅ GET /v1/buckets/default/lifecycle -> {len(data['transition_rules'])} transition rules")

    # Reset to no lifecycle
    status, _ = request("PUT", "/v1/buckets/default/lifecycle", body={"days": 0, "action": "soft_delete"})
    assert status == 200


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


def test_bucket_encryption():
    """Bucket encryption CRUD via REST API."""
    # Set AES256
    status, data = request("PUT", "/v1/buckets/default/encryption", body={"sse_algorithm": "AES256"})
    assert status == 200, f"PutEncryption: got {status}"
    print(f"  ✅ PUT /v1/buckets/default/encryption -> 200")

    # Read back
    status, data = request("GET", "/v1/buckets/default/encryption")
    assert status == 200
    assert data.get("sse_algorithm") == "AES256", f"expected AES256 got {data}"
    print(f"  ✅ GET /v1/buckets/default/encryption -> AES256")

    # Delete
    status, _ = request("DELETE", "/v1/buckets/default/encryption")
    assert status == 200, f"DeleteEncryption: got {status}"
    print(f"  ✅ DELETE /v1/buckets/default/encryption -> 200")

    # Confirm gone
    status, data = request("GET", "/v1/buckets/default/encryption")
    assert status == 200
    assert data.get("sse_algorithm") == "", f"expected empty got {data}"
    print(f"  ✅ GET /v1/buckets/default/encryption -> empty")


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


def test_bucket_notifications():
    """Bucket notification rule CRUD."""
    # Set notification rule with EndpointURL
    rules = [{
        "Id": "test-notif",
        "Events": ["s3:ObjectCreated:*"],
        "EndpointUrl": "http://localhost:9999/notif",
        "FilterKey": "",
    }]
    status, data = request("PUT", "/v1/buckets/default/notification", body={"rules": rules})
    assert status == 200, f"PutNotification: {status} {data}"
    print(f"  ✅ PUT /v1/buckets/default/notification -> 200")

    # Read back
    status, data = request("GET", "/v1/buckets/default/notification")
    assert status == 200, f"GetNotification: {status}"
    assert len(data.get("rules", [])) >= 1, f"expected rules, got {data}"
    print(f"  ✅ GET /v1/buckets/default/notification -> {len(data['rules'])} rule(s)")

    # Delete
    status, _ = request("DELETE", "/v1/buckets/default/notification")
    assert status == 200, f"DeleteNotification: {status}"
    print(f"  ✅ DELETE /v1/buckets/default/notification -> 200")


def test_mcp():
    """MCP protocol: tools/list and resources/list."""
    import json
    body = json.dumps({"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}}).encode()
    req = urllib.request.Request(BASE_URL + "/mcp", data=body, method="POST")
    req.add_header("Content-Type", "application/json")
    resp = urllib.request.urlopen(req, timeout=5)
    result = json.loads(resp.read())
    tools = result.get("result", {}).get("tools", [])
    names = [t["name"] for t in tools]
    assert "list_files" in names, f"MCP tools: {names}"
    print(f"  ✅ MCP tools/list -> {len(tools)} tools")

    # resources/list
    body2 = json.dumps({"jsonrpc": "2.0", "id": 2, "method": "resources/list", "params": {}}).encode()
    req2 = urllib.request.Request(BASE_URL + "/mcp", data=body2, method="POST")
    req2.add_header("Content-Type", "application/json")
    resp2 = urllib.request.urlopen(req2, timeout=5)
    json.loads(resp2.read())
    print(f"  ✅ MCP resources/list -> OK")


# ── Main ───────────────────────────────────────────────────────────────────

def test_edge_lifecycle_invalid():
    """Edge case: lifecycle with missing fields."""
    # Transition rule without required storage_class
    status, data = request("PUT", "/v1/buckets/default/lifecycle", body={
        "days": 0,
        "transition_rules": [{"days": 30}],
    })
    assert status == 200, f"missing storage_class in transition: {status}"
    print(f"  ✅ Lifecycle with incomplete transition rules accepted")

    # Reset
    request("PUT", "/v1/buckets/default/lifecycle", body={"days": 0})


def test_edge_encryption_roundtrip():
    """Edge case: AWS KMS-style encryption key."""
    # Set aws:kms with a key ID
    status, data = request("PUT", "/v1/buckets/default/encryption", body={
        "sse_algorithm": "aws:kms",
        "sse_kms_key_id": "arn:aws:kms:us-east-1:123456789012:key/abc123",
    })
    assert status == 200, f"PutEncryption kms: {status}"

    # Read back
    status, data = request("GET", "/v1/buckets/default/encryption")
    assert status == 200
    assert data.get("sse_algorithm") == "aws:kms", f"expected aws:kms got {data}"
    assert "abc123" in data.get("sse_kms_key_id", ""), f"expected key id in {data}"
    print(f"  ✅ KMS encryption roundtrip -> OK")

    # Reset
    request("DELETE", "/v1/buckets/default/encryption")


def test_mcp_tools_write_delete():
    """MCP write_file + delete_file tool integration."""
    # write_file
    body = json.dumps({
        "jsonrpc": "2.0", "id": 1, "method": "tools/call",
        "params": {
            "name": "write_file",
            "arguments": {"key": "mcp-e2e-write.txt", "content": "MCP test content"},
        },
    }).encode()
    req = urllib.request.Request(BASE_URL + "/mcp", data=body, method="POST")
    req.add_header("Content-Type", "application/json")
    resp = urllib.request.urlopen(req, timeout=5)
    result = json.loads(resp.read())
    assert "error" not in result, f"write_file failed: {result.get('error')}"
    print(f"  ✅ MCP write_file -> OK")

    # delete_file
    body2 = json.dumps({
        "jsonrpc": "2.0", "id": 2, "method": "tools/call",
        "params": {
            "name": "delete_file",
            "arguments": {"key": "mcp-e2e-write.txt"},
        },
    }).encode()
    req2 = urllib.request.Request(BASE_URL + "/mcp", data=body2, method="POST")
    req2.add_header("Content-Type", "application/json")
    resp2 = urllib.request.urlopen(req2, timeout=5)
    result2 = json.loads(resp2.read())
    assert "error" not in result2, f"delete_file failed: {result2.get('error')}"
    print(f"  ✅ MCP delete_file -> OK")


def test_bucket_versioning_policy():
    """Versioning toggle and verify it persists."""
    # Enable versioning
    status, _ = request("PUT", "/v1/buckets/default/versioning", body={"enabled": True})
    assert status == 200

    # Verify via config
    status, data = request("GET", "/v1/buckets/default/config")
    assert status == 200
    assert data.get("versioning") == True or data.get("versioning") == "true", f"versioning not enabled: {data}"
    print(f"  ✅ Bucket versioning toggle -> OK")

    # Disable versioning
    status, _ = request("PUT", "/v1/buckets/default/versioning", body={"enabled": False})
    assert status == 200
    print(f"  ✅ Bucket versioning disable -> OK")


ALL_TESTS = [
    ("Health", [test_healthz, test_readyz]),
    ("CRUD", [test_crud_roundtrip, test_list_objects]),
    ("Tags", [test_tags]),
    ("Multipart", [test_multipart]),
    ("Buckets", [test_bucket_crud, test_bucket_encryption, test_bucket_policy, test_cors,
                  test_bucket_notifications,
                  test_edge_lifecycle_invalid, test_edge_encryption_roundtrip,
                  test_bucket_versioning_policy]),
    ("MCP", [test_mcp, test_mcp_tools_write_delete]),
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
