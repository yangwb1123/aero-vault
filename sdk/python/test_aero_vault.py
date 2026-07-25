"""Offline tests for the aero_vault client — no server required.

Run:  python -m unittest test_aero_vault   (from sdk/python/)
The HTTP layer (Client._open) is stubbed so we assert exactly what request the
client builds (method, URL, headers, body) and how it decodes responses.
"""
import io
import json
import unittest
import urllib.error

import aero_vault as av


class FakeResp(io.BytesIO):
    """Minimal stand-in for an http.client.HTTPResponse."""

    def __init__(self, body=b"", headers=None, lines=None):
        super().__init__(body)
        self.headers = headers or {}
        self._lines = lines

    def __enter__(self):
        return self

    def __exit__(self, *a):
        self.close()

    def __iter__(self):
        # for SSE streaming, yield framed byte lines
        return iter(self._lines or [])


class StubClient(av.Client):
    """Records the last request and returns a queued response."""

    def __init__(self, response=None, **kw):
        super().__init__("http://test", **kw)
        self.calls = []
        self._response = response or FakeResp(b"{}")

    def _open(self, method, path, *, params=None, data=None, headers=None):
        self.calls.append({"method": method, "path": path, "params": params,
                           "data": data, "headers": self._headers(headers)})
        return self._response


class UploadTests(unittest.TestCase):
    def test_upload_sets_inferred_content_type_and_raw_body(self):
        c = StubClient(FakeResp(json.dumps({"key": "a.txt", "size": 3}).encode()))
        obj = c.upload("docs/a.txt", b"abc")
        call = c.calls[-1]
        self.assertEqual(call["method"], "PUT")
        self.assertEqual(call["path"], "/v1/files/docs/a.txt")
        self.assertEqual(call["data"], b"abc")                 # raw body, not form-encoded
        self.assertEqual(call["headers"]["Content-Type"], "text/plain")  # inferred from .txt
        self.assertEqual(obj.key, "a.txt")

    def test_upload_explicit_content_type_and_metadata(self):
        c = StubClient(FakeResp(b"{}"))
        c.upload("blob.bin", b"\x00\x01", content_type="application/pdf", metadata={"team": "x"})
        h = c.calls[-1]["headers"]
        self.assertEqual(h["Content-Type"], "application/pdf")
        self.assertEqual(h["X-Meta-team"], "x")

    def test_upload_unknown_extension_falls_back_to_octet_stream(self):
        c = StubClient(FakeResp(b"{}"))
        c.upload("weird.xyzzy", b"q")
        self.assertEqual(c.calls[-1]["headers"]["Content-Type"], "application/octet-stream")

    def test_str_body_is_encoded(self):
        c = StubClient(FakeResp(b"{}"))
        c.upload("a.txt", "héllo")
        self.assertEqual(c.calls[-1]["data"], "héllo".encode("utf-8"))


class JSONRequestTests(unittest.TestCase):
    def test_search_sends_json(self):
        c = StubClient(FakeResp(json.dumps({"hits": [{"object_key": "k", "score": 1.0}]}).encode()))
        hits = c.search("q", k=2, mode="hybrid")
        call = c.calls[-1]
        self.assertEqual(call["method"], "POST")
        self.assertEqual(call["path"], "/v1/search")
        self.assertEqual(call["headers"]["Content-Type"], "application/json")
        self.assertEqual(json.loads(call["data"]), {"query": "q", "k": 2, "mode": "hybrid"})
        self.assertEqual(hits[0].object_key, "k")

    def test_chat_parses_response(self):
        c = StubClient(FakeResp(json.dumps({"answer": "hi", "model": "m", "citations": [{"object_key": "d"}]}).encode()))
        r = c.chat("q")
        self.assertEqual(r.answer, "hi")
        self.assertEqual(len(r.citations), 1)

    def test_tenant_and_auth_headers(self):
        c = StubClient(FakeResp(b"{}"), token="secret", tenant="acme")
        c.list()
        h = c.calls[-1]["headers"]
        self.assertEqual(h["Authorization"], "Bearer secret")
        self.assertEqual(h["X-Aero-Tenant"], "acme")


class ContractTests(unittest.TestCase):
    """Request shapes that must match the server REST contract."""

    def test_lock_sends_seconds(self):
        c = StubClient(FakeResp(b"{}"))
        c.lock("docs/a.txt", 3600)
        call = c.calls[-1]
        self.assertEqual(call["method"], "POST")
        self.assertEqual(call["path"], "/v1/files/docs/a.txt/lock")
        self.assertEqual(json.loads(call["data"]), {"seconds": 3600})

    def test_add_key_includes_token_tenant_scopes(self):
        c = StubClient(FakeResp(b"{}"), tenant="acme")
        c.add_key("secret-tok", ["read", "write"], label="ci")
        call = c.calls[-1]
        self.assertEqual(call["method"], "POST")
        self.assertEqual(call["path"], "/v1/admin/keys")
        self.assertEqual(
            json.loads(call["data"]),
            {"token": "secret-tok", "tenant": "acme", "scopes": ["read", "write"], "label": "ci"},
        )

    def test_issue_jwt_sends_ttl_seconds(self):
        c = StubClient(FakeResp(b"{}"))
        c.issue_jwt("acme", scopes=["read"], ttl_seconds=120)
        body = json.loads(c.calls[-1]["data"])
        self.assertEqual(body["ttl_seconds"], 120)
        self.assertNotIn("expires_in", body)

    def test_delete_tags(self):
        c = StubClient(FakeResp(b""))
        c.delete_tags("docs/a.txt")
        call = c.calls[-1]
        self.assertEqual(call["method"], "DELETE")
        self.assertEqual(call["path"], "/v1/files/docs/a.txt/tags")

    def test_get_bucket_acl(self):
        c = StubClient(FakeResp(json.dumps({"acl": "public-read"}).encode()))
        acl = c.get_bucket_acl("my-bucket")
        self.assertEqual(c.calls[-1]["method"], "GET")
        self.assertEqual(c.calls[-1]["path"], "/v1/buckets/my-bucket/acl")
        self.assertEqual(acl, "public-read")

    def test_set_bucket_acl(self):
        c = StubClient(FakeResp(json.dumps({"acl": "private"}).encode()))
        c.set_bucket_acl("my-bucket", "private")
        call = c.calls[-1]
        self.assertEqual(call["method"], "PUT")
        self.assertEqual(call["path"], "/v1/buckets/my-bucket/acl")
        self.assertEqual(json.loads(call["data"]), {"acl": "private"})

    def test_revoke_key_escapes_token(self):
        c = StubClient(FakeResp(b""))
        c.revoke_key("a/b token")
        self.assertEqual(c.calls[-1]["path"], "/v1/admin/keys/a%2Fb%20token")

    def test_delete_tenant_escapes_id(self):
        c = StubClient(FakeResp(b""))
        c.delete_tenant("a/b")
        self.assertEqual(c.calls[-1]["path"], "/v1/admin/tenants/a%2Fb")


class ErrorTests(unittest.TestCase):
    def test_http_error_maps_to_aerovaulterror(self):
        body = json.dumps({"error": {"code": "NotFound", "message": "nope", "request_id": "r1"}}).encode()
        err = urllib.error.HTTPError("http://test", 404, "Not Found", {}, io.BytesIO(body))
        e = av.Client._to_error(err)
        self.assertEqual(e.status, 404)
        self.assertEqual(e.code, "NotFound")
        self.assertEqual(e.request_id, "r1")


class StreamTests(unittest.TestCase):
    def test_chat_stream_yields_tokens_and_calls_on_done(self):
        lines = [
            b"event: token\n", b'data: "Hello"\n', b"\n",
            b"event: token\n", b'data: " world"\n', b"\n",
            b"event: done\n", b'data: {"answer":"Hello world","citations":[]}\n', b"\n",
        ]
        c = StubClient(FakeResp(lines=lines))
        done = {}
        toks = list(c.chat_stream("q", on_done=lambda r: done.update(answer=r.answer)))
        self.assertEqual(toks, ["Hello", " world"])
        self.assertEqual(done["answer"], "Hello world")

    def test_key_escaping(self):
        self.assertEqual(av._escape_key("/a/b c.txt"), "a/b%20c.txt")


if __name__ == "__main__":
    unittest.main()
