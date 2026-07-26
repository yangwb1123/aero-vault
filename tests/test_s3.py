#!/usr/bin/env python3
"""S3-gateway-specific tests via the /s3 prefix."""
import json, os, sys, urllib.error, urllib.request, urllib.parse, uuid, xml.etree.ElementTree as ET

BASE_URL = os.environ.get("BASE_URL", "http://localhost:8080")
S3_PREFIX = "/s3"
TIMEOUT = 15


def s3_req(method, path, body=None, headers=None, ct=None):
    url = BASE_URL + S3_PREFIX + path
    data = None
    if body is not None:
        data = body.encode() if isinstance(body, str) else body if isinstance(body, bytes) else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method)
    if ct:
        req.add_header("Content-Type", ct)
    if headers:
        for k, v in headers.items():
            req.add_header(k, v)
    try:
        resp = urllib.request.urlopen(req, timeout=TIMEOUT)
        raw = resp.read()
        return resp.status, raw
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def test_put_get_delete():
    key = "s3test/obj.txt"
    assert s3_req("PUT", f"/default/{key}", body=b"data", ct="text/plain")[0] in (200, 201)
    st, body = s3_req("GET", f"/default/{key}")
    assert st == 200 and b"data" in body
    s3_req("DELETE", f"/default/{key}")
    print("  ✅ S3 PUT/GET/DELETE")


def test_head_etag():
    key = "s3test/head.txt"
    s3_req("PUT", f"/default/{key}", body=b"x", ct="text/plain")
    url = BASE_URL + S3_PREFIX + f"/default/{key}"
    req = urllib.request.Request(url, method="HEAD")
    resp = urllib.request.urlopen(req, timeout=TIMEOUT)
    assert resp.headers.get("ETag", ""), "missing ETag"
    resp.close()
    s3_req("DELETE", f"/default/{key}")
    print("  ✅ S3 HEAD ETag")


def test_list_v2():
    prefix = "s3test/lv2/"
    for i in range(3):
        s3_req("PUT", f"/default/{prefix}{i}.txt", body=f"d{i}".encode(), ct="text/plain")
    st, body = s3_req("GET", f"/default/?list-type=2&prefix={prefix}")
    assert st == 200
    root = ET.fromstring(body.decode())
    assert len(root.findall(".//{*}Contents")) == 3
    for i in range(3):
        s3_req("DELETE", f"/default/{prefix}{i}.txt")
    print("  ✅ S3 ListObjectsV2")


def test_multipart():
    key = f"s3test/multi-{uuid.uuid4().hex[:8]}.bin"
    st, body = s3_req("POST", f"/default/{key}?uploads")
    assert st == 200
    root = ET.fromstring(body.decode())
    uid = root.find(".//{*}UploadId").text

    put_url = f"{BASE_URL}/s3/default/{key}?partNumber=1&uploadId={uid}"
    preq = urllib.request.Request(put_url, data=b"x"*100, method="PUT")
    presp = urllib.request.urlopen(preq, timeout=TIMEOUT)
    etag = presp.headers.get("ETag", '"fallback"')
    presp.close()

    xml = f"<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>{etag}</ETag></Part></CompleteMultipartUpload>"
    st, body = s3_req("POST", f"/default/{key}?uploadId={uid}", body=xml.encode(), ct="application/xml")
    assert st == 200, f"complete: {st} {body[:200]}"
    s3_req("DELETE", f"/default/{key}")
    print("  ✅ S3 multipart")


def test_bucket_encryption_s3_api():
    key = "s3enc/test-enc.txt"
    # Put object then set encryption via S3-syntax PUT /{bucket}?encryption
    s3_req("PUT", f"/default/{key}", body=b"encryptable data", ct="text/plain")

    # Set AES256 encryption
    xml = """<?xml version="1.0" encoding="UTF-8"?>
<ServerSideEncryptionConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Rule>
    <ApplyServerSideEncryptionByDefault>
      <SSEAlgorithm>AES256</SSEAlgorithm>
    </ApplyServerSideEncryptionByDefault>
  </Rule>
</ServerSideEncryptionConfiguration>"""
    st, _ = s3_req("PUT", "/default/?encryption", body=xml, ct="application/xml")
    assert st == 200, f"put encryption: {st}"

    # Read it back
    st, body = s3_req("GET", "/default/?encryption")
    assert st == 200, f"get encryption: {st}"
    root = ET.fromstring(body.decode())
    alg = root.find(".//{*}SSEAlgorithm").text
    assert alg == "AES256", f"expected AES256 got {alg}"

    # Delete encryption
    st, _ = s3_req("DELETE", "/default/?encryption")
    assert st == 204, f"delete encryption: {st}"

    # Confirm gone
    st, _ = s3_req("GET", "/default/?encryption")
    assert st == 404, f"expected 404 after delete got {st}"

    # Cleanup
    s3_req("DELETE", f"/default/{key}")
    print("  ✅ S3 bucket encryption")


def test_upload_part_copy():
    """Test UploadPartCopy via S3 API."""
    src_key = f"s3test/upc-src-{uuid.uuid4().hex[:8]}.bin"
    dst_key = f"s3test/upc-dst-{uuid.uuid4().hex[:8]}.bin"

    # Put a 500-byte source object
    body = b"x" * 500
    st, _ = s3_req("PUT", f"/default/{src_key}", body=body, ct="application/octet-stream")
    assert st in (200, 201), f"put src: {st}"

    # Initiate multipart upload
    st, body = s3_req("POST", f"/default/{dst_key}?uploads")
    assert st == 200, f"init multipart: {st}"
    root = ET.fromstring(body.decode())
    uid = root.find(".//{*}UploadId").text

    # UploadPartCopy: copy first 200 bytes from source
    copy_src = f"/default/{src_key}"
    headers = {
        "x-amz-copy-source": copy_src,
        "x-amz-copy-source-range": "bytes=0-199",
    }
    st, body = s3_req("PUT", f"/default/{dst_key}?partNumber=1&uploadId={uid}",
                       body=b"", headers=headers)
    assert st == 200, f"upload part copy: {st}"
    root2 = ET.fromstring(body.decode())
    etag1 = root2.find(".//{*}ETag").text.strip('"')

    # UploadPartCopy: copy bytes 200-499
    headers["x-amz-copy-source-range"] = "bytes=200-499"
    st, body = s3_req("PUT", f"/default/{dst_key}?partNumber=2&uploadId={uid}",
                       body=b"", headers=headers)
    assert st == 200, f"upload part copy 2: {st}"
    root3 = ET.fromstring(body.decode())
    etag2 = root3.find(".//{*}ETag").text.strip('"')

    # Complete multipart
    xml = f"""<CompleteMultipartUpload>
  <Part><PartNumber>1</PartNumber><ETag>"{etag1}"</ETag></Part>
  <Part><PartNumber>2</PartNumber><ETag>"{etag2}"</ETag></Part>
</CompleteMultipartUpload>"""
    st, body = s3_req("POST", f"/default/{dst_key}?uploadId={uid}",
                      body=xml.encode(), ct="application/xml")
    assert st == 200, f"complete: {st} {body[:200]}"

    # Verify destination size
    st, body = s3_req("GET", f"/default/{dst_key}")
    assert st == 200, f"get dst: {st}"
    assert len(body) == 500, f"expected 500 bytes got {len(body)}"

    # Cleanup
    s3_req("DELETE", f"/default/{src_key}")
    s3_req("DELETE", f"/default/{dst_key}")
    print("  ✅ S3 UploadPartCopy")


ALL_TESTS = [
    ("S3 CRUD", [test_put_get_delete, test_head_etag]),
    ("S3 List", [test_list_v2]),
    ("S3 Multipart", [test_multipart]),
    ("S3 Encryption", [test_bucket_encryption_s3_api]),
    ("S3 UploadPartCopy", [test_upload_part_copy]),
]


def main():
    passed = failed = 0
    for grp, tests in ALL_TESTS:
        print(f"\n--- {grp} ---")
        for fn in tests:
            try:
                fn()
                passed += 1
            except Exception as e:
                import traceback
                print(f"  ❌ {fn.__name__}: {e}")
                traceback.print_exc()
                failed += 1
    print(f"\n  {passed}/{passed+failed} passed" + (" ✅ ALL PASSED" if not failed else f" ❌ {failed} FAILED"))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
