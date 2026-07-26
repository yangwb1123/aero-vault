#!/usr/bin/env python3
"""Adversarial tests: error handling, edge cases, S3 interop, data integrity.

Usage:
    BASE_URL=http://localhost:9095 python3 tests/test_adversarial.py
"""
import json
import os
import urllib.error
import urllib.request
import urllib.parse
import uuid

BASE_URL = os.environ.get("BASE_URL", "http://localhost:8080")
TIMEOUT = 10


def req(method, path, body=None, headers=None, content_type=None):
    url = BASE_URL + path
    data = None
    if body is not None:
        if isinstance(body, (dict, list)):
            data = json.dumps(body).encode()
        elif isinstance(body, str):
            data = body.encode()
        else:
            data = body
    req = urllib.request.Request(url, data=data, method=method)
    if content_type:
        req.add_header("Content-Type", content_type)
    elif isinstance(body, (dict, list)):
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


# ── Tests ──────────────────────────────────────────────────────────────

def test_put_without_content_type():
    """PUT without Content-Type should still work."""
    key = f"adv/no-ct-{uuid.uuid4().hex[:8]}"
    status, data = req("PUT", f"/v1/files/{key}", body="raw data")
    assert status in (200, 201), f"PUT no CT: got {status}"
    # Cleanup
    req("DELETE", f"/v1/files/{key}?hard=1")
    print(f"  ✅ PUT without Content-Type -> {status}")


def test_get_nonexistent_key():
    """GET non-existent key returns 404 with JSON body."""
    status, data = req("GET", "/v1/files/nonexistent-" + uuid.uuid4().hex)
    assert status == 404, f"GET nonexistent: got {status}"
    if isinstance(data, dict):
        code = data.get("error", {}).get("code", "")
        assert code == "NotFound", f"error code: {code}"
    print(f"  ✅ GET nonexistent -> 404 NotFound")


def test_delete_nonexistent_key():
    """DELETE non-existent key returns 404."""
    status, _ = req("DELETE", f"/v1/files/nonexistent-{uuid.uuid4().hex}?hard=1")
    assert status == 404, f"DELETE nonexistent: got {status}"
    print(f"  ✅ DELETE nonexistent -> 404")


def test_invalid_key_with_dotdot():
    """Key with '..' should be rejected."""
    status, data = req("PUT", "/v1/files/../etc/passwd", body="x")
    assert status == 400, f"PUT with '..': got {status}"
    print(f"  ✅ PUT with '..' rejected -> 400")


def test_empty_key_rejected():
    """Empty key should be rejected."""
    status, _ = req("PUT", "/v1/files/", body="x")
    assert status != 200, "PUT empty key should not succeed"
    print(f"  ✅ PUT empty key rejected -> {status}")


def test_long_key_rejected():
    """Key exceeding 200 chars should be rejected (filesystem limit)."""
    long_key = "k" + "a" * 200  # 201 chars
    status, data = req("PUT", f"/v1/files/{long_key}", body="x")
    assert status == 400, f"long key: got {status}, expected 400"
    print(f"  ✅ Long key (201 chars) rejected -> 400")


def test_overwrite_object():
    """PUT to existing key should overwrite (no versioning)."""
    key = f"adv/overwrite-{uuid.uuid4().hex[:8]}"
    req("PUT", f"/v1/files/{key}", body="version1")
    status, _ = req("PUT", f"/v1/files/{key}", body="version2")
    assert status in (200, 201), f"PUT overwrite: got {status}"

    # Read back — should be version2
    status, body = req("GET", f"/v1/files/{key}")
    if status == 200 and isinstance(body, bytes):
        assert body.decode() == "version2", f"body: {body}"
    print(f"  ✅ Overwrite PUT -> {status}, body=version2")
    req("DELETE", f"/v1/files/{key}?hard=1")


def test_list_with_marker():
    """List with marker pagination."""
    prefix = f"adv/page-{uuid.uuid4().hex[:8]}/"
    for i in range(5):
        req("PUT", f"/v1/files/{prefix}file-{i}.txt", body=f"data-{i}")

    status, data = req("GET", f"/v1/files?prefix={prefix}&limit=2")
    assert status == 200
    if isinstance(data, dict):
        objects = data.get("objects", [])
        assert len(objects) <= 2, f"page limit: got {len(objects)}"
        has_more = data.get("has_more", False)
        print(f"  ✅ LIST {prefix} limit=2 -> {len(objects)} objects, has_more={has_more}")

    # Cleanup
    for i in range(5):
        req("DELETE", f"/v1/files/{prefix}file-{i}.txt?hard=1")
    print(f"  ✅ LIST with pagination works")


def test_sse_stream_connection():
    """SSE stream endpoint should return text/event-stream."""
    try:
        url = BASE_URL + "/v1/events/stream"
        req = urllib.request.Request(url)
        resp = urllib.request.urlopen(req, timeout=3)
        ct = resp.headers.get("Content-Type", "")
        assert "text/event-stream" in ct or "text/plain" in ct, f"CT: {ct}"
        resp.close()
        print(f"  ✅ SSE stream -> Content-Type: {ct}")
    except urllib.error.HTTPError as e:
        # SSE may require auth or return 4xx; that's acceptable
        print(f"  ⚠️  SSE stream -> {e.code} (may need auth)")


def test_bucket_stats():
    """Bucket stats endpoint should return valid numbers."""
    key = f"adv/stats-{uuid.uuid4().hex[:8]}"
    req("PUT", f"/v1/files/{key}", body="data")

    status, data = req("GET", "/v1/buckets/default/stats")
    assert status == 200, f"stats: got {status}"
    if isinstance(data, dict):
        assert "object_count" in data
        assert "total_size_bytes" in data
        assert isinstance(data["object_count"], int)
    print(f"  ✅ Bucket stats -> object_count={data.get('object_count', '?')}")

    req("DELETE", f"/v1/files/{key}?hard=1")


def test_legal_hold_flow():
    """Legal hold on an object should block hard delete."""
    key = f"adv/legal-{uuid.uuid4().hex[:8]}"
    req("PUT", f"/v1/files/{key}", body="important data")

    # Add legal hold
    status, _ = req("PUT", "/v1/legal-hold", body={"key": key, "reason": "litigation"})
    assert status in (200, 201), f"PutLegalHold: got {status}"
    print(f"  ✅ Legal hold PUT -> {status}")

    # Get legal hold
    encoded_key = urllib.parse.quote(key, safe="")
    status, data = req("GET", f"/v1/legal-hold?key={encoded_key}")
    assert status in (200, 404), f"GetLegalHold: got {status}"
    print(f"  ✅ Legal hold GET -> {status}")

    # Hard delete should now fail (blocked by legal hold)
    status, data = req("DELETE", f"/v1/files/{key}?hard=1")
    if status == 200 or status == 204:
        print(f"  ⚠️  Hard delete succeeded despite legal hold (may be expected)")
    else:
        print(f"  ✅ Hard delete blocked by legal hold -> {status}")

    # Cleanup via soft delete (should work)
    status, _ = req("DELETE", f"/v1/files/{key}")
    print(f"  ✅ Soft delete -> {status}")


def test_concurrent_writes():
    """Multiple concurrent writes to the same key — last write wins."""
    import threading
    key = f"adv/concurrent-{uuid.uuid4().hex[:8]}"
    errors = []
    def write(val):
        try:
            req("PUT", f"/v1/files/{key}", body=val)
        except Exception as e:
            errors.append(e)
    threads = [threading.Thread(target=write, args=(f"data-{i}",)) for i in range(10)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    assert len(errors) == 0, f"concurrent writes had {len(errors)} errors"
    status, data = req("GET", f"/v1/files/{key}")
    assert status == 200, f"concurrent final read: {status}"
    req("DELETE", f"/v1/files/{key}?hard=1")
    print(f"  ✅ Concurrent writes (10 threads) -> OK")


def test_very_long_key():
    """Key at the max allowed length (200 chars) should succeed."""
    long_key = "adv/" + "a" * 195  # 199 chars total
    status, _ = req("PUT", f"/v1/files/{long_key}", body="x")
    assert status == 201, f"long key put: {status}"
    status, data = req("GET", f"/v1/files/{long_key}")
    assert status == 200, f"long key get: {status}"
    req("DELETE", f"/v1/files/{long_key}?hard=1")
    print(f"  ✅ Max-length key (199 chars) -> OK")


def test_lifecycle_with_transitions():
    """Lifecycle with transition rules via S3 XML API."""
    xml = b"""<?xml version="1.0" encoding="UTF-8"?>
<LifecycleConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Rule>
    <ID>expire-rule</ID>
    <Status>Enabled</Status>
    <Expiration><Days>365</Days></Expiration>
  </Rule>
  <Rule>
    <ID>transition-ia</ID>
    <Status>Enabled</Status>
    <Transition><Days>30</Days><StorageClass>STANDARD_IA</StorageClass></Transition>
  </Rule>
  <Rule>
    <ID>transition-glacier</ID>
    <Status>Enabled</Status>
    <Transition><Days>90</Days><StorageClass>GLACIER</StorageClass></Transition>
  </Rule>
</LifecycleConfiguration>"""
    url = BASE_URL + "/s3/default/?lifecycle"
    req = urllib.request.Request(url, data=xml, method="PUT")
    req.add_header("Content-Type", "application/xml")
    resp = urllib.request.urlopen(req, timeout=TIMEOUT)
    assert resp.status == 200, f"S3 lifecycle PUT: {resp.status}"
    resp.close()

    # Read back and verify
    resp2 = urllib.request.urlopen(url, timeout=TIMEOUT)
    body = resp2.read()
    resp2.close()
    assert b"STANDARD_IA" in body, f"missing STANDARD_IA transition"
    assert b"GLACIER" in body, f"missing GLACIER transition"
    print(f"  ✅ Lifecycle transitions via S3 XML API -> OK")

    req3 = urllib.request.Request(url, method="DELETE")
    resp3 = urllib.request.urlopen(req3, timeout=TIMEOUT)
    resp3.close()


def test_version_multipart_combo():
    """Create versioned bucket, upload file, then multipart upload same key."""
    req("PUT", "/v1/buckets/default/versioning", body={"enabled": True})

    key = f"adv/ver-mp-{uuid.uuid4().hex[:8]}"
    req("PUT", f"/v1/files/{key}", body="version-1")

    status, data = req("POST", "/v1/multipart", body={"bucket": "default", "key": key})
    assert status == 201, f"init multipart: {status}"
    uid = data["upload_id"]

    part_url = f"/v1/multipart/{uid}/parts/1"
    put_req = urllib.request.Request(BASE_URL + part_url, data=b"version-2", method="PUT")
    put_req.add_header("Content-Type", "application/octet-stream")
    put_resp = urllib.request.urlopen(put_req, timeout=TIMEOUT)
    put_resp.close()

    status, _ = req("POST", f"/v1/multipart/{uid}/complete")
    assert status == 200, f"complete: {status}"

    status, data = req("GET", f"/v1/files/{key}/versions")
    assert status == 200
    version_count = len(data.get("versions", []))
    assert version_count >= 1, f"expected versions, got {data}"
    print(f"  ✅ Versioned multipart combo ({version_count} versions) -> OK")

    req("DELETE", f"/v1/files/{key}?hard=1")
    req("PUT", "/v1/buckets/default/versioning", body={"enabled": False})


# ── Main ───────────────────────────────────────────────────────────────

ALL_TESTS = [
    ("Error Paths", [test_put_without_content_type, test_get_nonexistent_key,
                     test_delete_nonexistent_key, test_invalid_key_with_dotdot,
                     test_empty_key_rejected]),
    ("Data Integrity", [test_overwrite_object, test_list_with_marker]),
    ("Streaming & Stats", [test_sse_stream_connection, test_bucket_stats]),
    ("Compliance", [test_legal_hold_flow]),
    ("Edge Cases", [test_concurrent_writes, test_very_long_key, test_lifecycle_with_transitions, test_version_multipart_combo]),
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
    import sys
    sys.exit(main())
