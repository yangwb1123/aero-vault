"""aero_vault — Python client for the aero-vault AI-native file platform.

Zero third-party dependencies: the client is built on the standard library
(``urllib``) so it works either installed (``pip install aero-vault``) or copied
into a project and imported directly (``import aero_vault``).

Quickstart::

    from aero_vault import Client

    av = Client("http://localhost:8080", token="prod-rw", tenant="acme")
    av.upload("docs/readme.txt", b"hello world", content_type="text/plain")
    print(av.get("docs/readme.txt"))            # b"hello world"
    for obj in av.list(prefix="docs/"):
        print(obj.key, obj.size)
    print(av.search("hello", k=5))
    print(av.chat("what does the readme say?")["answer"])
    for token in av.chat_stream("summarize the docs"):
        print(token, end="", flush=True)
"""

from __future__ import annotations

import json
import mimetypes
import os
import typing as t
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field

__version__ = "0.4.0"
__all__ = ["Client", "AeroVaultError", "Object", "SearchHit", "ChatResponse"]

# A body may be raw bytes, text, or any object exposing ``.read()``.
Readable = t.Union[bytes, bytearray, str, t.BinaryIO]


class AeroVaultError(RuntimeError):
    """Raised when the server returns a non-2xx response.

    The platform's error envelope is ``{"error":{"code","message","request_id"}}``;
    those fields are surfaced on the exception when present.
    """

    def __init__(self, status: int, code: str = "", message: str = "", request_id: str = ""):
        self.status = status
        self.code = code or "HTTPError"
        self.message = message or f"HTTP {status}"
        self.request_id = request_id
        suffix = f" (request_id={request_id})" if request_id else ""
        super().__init__(f"[{status} {self.code}] {self.message}{suffix}")


@dataclass
class Object:
    """A stored object's metadata, as returned by upload/list/stat."""

    bucket: str = ""
    key: str = ""
    size: int = 0
    etag: str = ""
    content_type: str = ""
    backend: str = ""
    metadata: t.Dict[str, str] = field(default_factory=dict)
    tags: t.Dict[str, str] = field(default_factory=dict)
    created_at: str = ""
    updated_at: str = ""

    @classmethod
    def from_json(cls, d: t.Mapping[str, t.Any]) -> "Object":
        return cls(
            bucket=d.get("bucket", ""),
            key=d.get("key", ""),
            size=int(d.get("size", 0) or 0),
            etag=d.get("etag", ""),
            content_type=d.get("content_type", ""),
            backend=d.get("backend", ""),
            metadata=dict(d.get("metadata") or {}),
            tags=dict(d.get("tags") or {}),
            created_at=d.get("created_at", ""),
            updated_at=d.get("updated_at", ""),
        )


@dataclass
class SearchHit:
    """One ranked result from /v1/search (also used as a chat citation)."""

    score: float = 0.0
    chunk: str = ""
    chunk_id: int = 0
    object_id: int = 0
    bucket: str = ""
    object_key: str = ""
    seq: int = 0
    embed_model: str = ""
    raw: t.Dict[str, t.Any] = field(default_factory=dict)

    @classmethod
    def from_json(cls, d: t.Mapping[str, t.Any]) -> "SearchHit":
        return cls(
            score=float(d.get("score", 0.0) or 0.0),
            chunk=d.get("chunk", ""),
            chunk_id=int(d.get("chunk_id", 0) or 0),
            object_id=int(d.get("object_id", 0) or 0),
            bucket=d.get("bucket", ""),
            object_key=d.get("object_key", ""),
            seq=int(d.get("seq", 0) or 0),
            embed_model=d.get("embed_model", ""),
            raw=dict(d),
        )


@dataclass
class ChatResponse:
    """Answer plus grounding citations from /v1/chat."""

    answer: str = ""
    model: str = ""
    citations: t.List[SearchHit] = field(default_factory=list)

    @classmethod
    def from_json(cls, d: t.Mapping[str, t.Any]) -> "ChatResponse":
        return cls(
            answer=d.get("answer", ""),
            model=d.get("model", ""),
            citations=[SearchHit.from_json(h) for h in (d.get("citations") or [])],
        )

    def __getitem__(self, k: str) -> t.Any:  # dict-style access for convenience
        return getattr(self, k)


class Client:
    """HTTP client for an aero-vault server.

    Args:
        base_url: Service root, e.g. ``http://localhost:8080``.
        token:    API key or JWT. Sent as ``Authorization: Bearer <token>``,
                  or as ``X-Api-Key`` when ``api_key_header=True``.
        tenant:   Value for the ``X-Aero-Tenant`` header (multi-tenancy).
        timeout:  Per-request timeout in seconds.
    """

    def __init__(
        self,
        base_url: str = "http://localhost:8080",
        token: t.Optional[str] = None,
        tenant: str = "default",
        timeout: float = 30.0,
        api_key_header: bool = False,
    ):
        self.base_url = base_url.rstrip("/")
        self.token = token or os.environ.get("AERO_VAULT_TOKEN") or None
        self.tenant = tenant
        self.timeout = timeout
        self.api_key_header = api_key_header

    # ---- low-level HTTP -------------------------------------------------

    def _headers(self, extra: t.Optional[t.Mapping[str, str]] = None) -> t.Dict[str, str]:
        h: t.Dict[str, str] = {"X-Aero-Tenant": self.tenant, "Accept": "application/json"}
        if self.token:
            if self.api_key_header:
                h["X-Api-Key"] = self.token
            else:
                h["Authorization"] = "Bearer " + self.token
        if extra:
            h.update({k: v for k, v in extra.items() if v is not None})
        return h

    def _url(self, path: str, params: t.Optional[t.Mapping[str, t.Any]] = None) -> str:
        url = self.base_url + path
        if params:
            clean = {k: v for k, v in params.items() if v not in (None, "")}
            if clean:
                url += "?" + urllib.parse.urlencode(clean)
        return url

    def _open(
        self,
        method: str,
        path: str,
        *,
        params: t.Optional[t.Mapping[str, t.Any]] = None,
        data: t.Optional[bytes] = None,
        headers: t.Optional[t.Mapping[str, str]] = None,
    ):
        req = urllib.request.Request(self._url(path, params), data=data, method=method)
        for k, v in self._headers(headers).items():
            req.add_header(k, v)
        try:
            return urllib.request.urlopen(req, timeout=self.timeout)
        except urllib.error.HTTPError as e:
            raise self._to_error(e) from None

    @staticmethod
    def _to_error(e: "urllib.error.HTTPError") -> AeroVaultError:
        body = b""
        try:
            body = e.read()
        except Exception:  # noqa: BLE001 - best-effort error body
            pass
        code = message = request_id = ""
        try:
            env = json.loads(body.decode("utf-8"))
            err = env.get("error", env) if isinstance(env, dict) else {}
            code, message = err.get("code", ""), err.get("message", "")
            request_id = err.get("request_id", "")
        except Exception:  # noqa: BLE001 - non-JSON error body (e.g. http.Error)
            message = body.decode("utf-8", "replace").strip()
        return AeroVaultError(e.code, code, message, request_id)

    def _request_json(
        self,
        method: str,
        path: str,
        *,
        params: t.Optional[t.Mapping[str, t.Any]] = None,
        json_body: t.Any = None,
        data: t.Optional[bytes] = None,
        headers: t.Optional[t.Mapping[str, str]] = None,
    ) -> t.Any:
        """Send a request and decode a JSON response (or None for empty bodies).

        Pass ``json_body`` to send JSON, or ``data`` to send a raw byte body
        (e.g. a file upload). ``data`` takes precedence over ``json_body``.
        """
        hdrs = dict(headers or {})
        if data is None and json_body is not None:
            data = json.dumps(json_body).encode("utf-8")
            hdrs.setdefault("Content-Type", "application/json")
        with self._open(method, path, params=params, data=data, headers=hdrs) as resp:
            raw = resp.read()
        if not raw:
            return None
        return json.loads(raw.decode("utf-8"))

    @staticmethod
    def _coerce_body(data: Readable) -> bytes:
        if isinstance(data, (bytes, bytearray)):
            return bytes(data)
        if isinstance(data, str):
            return data.encode("utf-8")
        if hasattr(data, "read"):
            chunk = data.read()
            return chunk.encode("utf-8") if isinstance(chunk, str) else bytes(chunk)
        raise TypeError("data must be bytes, str, or a readable file object")

    # ---- files ----------------------------------------------------------

    def upload(
        self,
        key: str,
        data: Readable,
        content_type: t.Optional[str] = None,
        metadata: t.Optional[t.Mapping[str, str]] = None,
    ) -> Object:
        """Upload raw bytes to ``key`` (PUT /v1/files/<key>).

        Content-Type is always sent: when not given it is inferred from the
        key's extension, falling back to application/octet-stream. (Sending no
        Content-Type lets urllib inject application/x-www-form-urlencoded, which
        the server's text extractor skips — so the object would never be
        indexed for search.)
        """
        body = self._coerce_body(data)
        if not content_type:
            content_type = mimetypes.guess_type(key)[0] or "application/octet-stream"
        headers: t.Dict[str, str] = {"Content-Type": content_type}
        for mk, mv in (metadata or {}).items():
            headers["X-Meta-" + mk] = mv
        out = self._request_json("PUT", "/v1/files/" + _escape_key(key), data=body, headers=headers)
        return Object.from_json(out or {})

    def upload_file(
        self,
        key: str,
        path: str,
        content_type: t.Optional[str] = None,
        metadata: t.Optional[t.Mapping[str, str]] = None,
    ) -> Object:
        """Upload a local file by path."""
        with open(path, "rb") as f:
            return self.upload(key, f.read(), content_type=content_type, metadata=metadata)

    def get(self, key: str, version: t.Optional[str] = None) -> bytes:
        """Download an object's bytes (GET /v1/files/<key>)."""
        with self._open("GET", "/v1/files/" + _escape_key(key), params={"version": version}) as resp:
            return resp.read()

    def download(self, key: str, dest_path: str, version: t.Optional[str] = None) -> int:
        """Stream an object to a local file; returns bytes written."""
        with self._open("GET", "/v1/files/" + _escape_key(key), params={"version": version}) as resp:
            n = 0
            with open(dest_path, "wb") as out:
                while True:
                    chunk = resp.read(64 * 1024)
                    if not chunk:
                        break
                    out.write(chunk)
                    n += len(chunk)
        return n

    def stat(self, key: str) -> Object:
        """HEAD an object; returns metadata derived from response headers."""
        with self._open("HEAD", "/v1/files/" + _escape_key(key)) as resp:
            h = resp.headers
            return Object(
                key=key,
                size=int(h.get("Content-Length", 0) or 0),
                etag=(h.get("ETag", "") or "").strip('"'),
                content_type=h.get("Content-Type", ""),
                updated_at=h.get("Last-Modified", ""),
            )

    def exists(self, key: str) -> bool:
        """True if the object exists (404 -> False)."""
        try:
            self.stat(key)
            return True
        except AeroVaultError as e:
            if e.status == 404:
                return False
            raise

    def list(
        self,
        prefix: t.Optional[str] = None,
        marker: t.Optional[str] = None,
        limit: t.Optional[int] = None,
    ) -> t.List[Object]:
        """List a single page of objects (GET /v1/files)."""
        out = self._request_json(
            "GET", "/v1/files", params={"prefix": prefix, "marker": marker, "limit": limit}
        )
        return [Object.from_json(o) for o in (out or {}).get("objects", [])]

    def iter_objects(
        self, prefix: t.Optional[str] = None, page_size: int = 1000
    ) -> t.Iterator[Object]:
        """Auto-paginate over every object under ``prefix``."""
        marker: t.Optional[str] = None
        while True:
            out = self._request_json(
                "GET", "/v1/files", params={"prefix": prefix, "marker": marker, "limit": page_size}
            ) or {}
            for o in out.get("objects", []):
                yield Object.from_json(o)
            if not out.get("has_more"):
                return
            marker = out.get("next_marker") or None
            if not marker:
                return

    def delete(self, key: str, hard: bool = False) -> None:
        """Delete an object (soft by default; ``hard=True`` removes bytes)."""
        with self._open(
            "DELETE", "/v1/files/" + _escape_key(key), params={"hard": "1" if hard else None}
        ):
            return None

    def presign(self, key: str, op: str = "get", expires: int = 900) -> t.Dict[str, t.Any]:
        """Create a presigned URL (op = ``get`` | ``put``)."""
        return self._request_json(
            "POST",
            "/v1/files/" + _escape_key(key) + "/presign",
            params={"op": op, "expires": expires},
        )

    # ---- tags / versions / lock ----------------------------------------

    def get_tags(self, key: str) -> t.Dict[str, str]:
        return self._request_json("GET", "/v1/files/" + _escape_key(key) + "/tags") or {}

    def put_tags(self, key: str, tags: t.Mapping[str, str]) -> t.Any:
        return self._request_json(
            "PUT", "/v1/files/" + _escape_key(key) + "/tags", json_body=dict(tags)
        )

    def list_versions(self, key: str) -> t.Any:
        return self._request_json("GET", "/v1/files/" + _escape_key(key) + "/versions")

    def lock(self, key: str, until: str) -> t.Any:
        """Apply an object lock retaining the object until an RFC3339 timestamp."""
        return self._request_json(
            "POST", "/v1/files/" + _escape_key(key) + "/lock", json_body={"until": until}
        )

    # ---- AI: search / chat / agent -------------------------------------

    def search(
        self,
        query: str,
        k: int = 10,
        mode: t.Optional[str] = None,
        bucket: t.Optional[str] = None,
    ) -> t.List[SearchHit]:
        """Semantic / hybrid search (mode = ``vector`` | ``bm25`` | ``hybrid``)."""
        body: t.Dict[str, t.Any] = {"query": query, "k": k}
        if mode:
            body["mode"] = mode
        if bucket:
            body["bucket"] = bucket
        out = self._request_json("POST", "/v1/search", json_body=body) or {}
        return [SearchHit.from_json(h) for h in (out.get("hits") or [])]

    def chat(
        self,
        query: str,
        k: t.Optional[int] = None,
        mode: t.Optional[str] = None,
        bucket: t.Optional[str] = None,
        temperature: t.Optional[float] = None,
        prior: t.Optional[t.List[t.Dict[str, str]]] = None,
    ) -> ChatResponse:
        """RAG chat with citations (POST /v1/chat)."""
        body: t.Dict[str, t.Any] = {"query": query}
        for name, val in (("k", k), ("mode", mode), ("bucket", bucket),
                          ("temperature", temperature), ("prior", prior)):
            if val is not None:
                body[name] = val
        out = self._request_json("POST", "/v1/chat", json_body=body) or {}
        return ChatResponse.from_json(out)

    def chat_stream(
        self,
        query: str,
        k: t.Optional[int] = None,
        mode: t.Optional[str] = None,
        bucket: t.Optional[str] = None,
        on_done: t.Optional[t.Callable[[ChatResponse], None]] = None,
    ) -> t.Iterator[str]:
        """Streaming RAG chat. Yields answer tokens as they arrive (SSE).

        If ``on_done`` is provided it is called with the final ChatResponse
        (answer + citations) once the stream completes.
        """
        body: t.Dict[str, t.Any] = {"query": query}
        for name, val in (("k", k), ("mode", mode), ("bucket", bucket)):
            if val is not None:
                body[name] = val
        data = json.dumps(body).encode("utf-8")
        resp = self._open("POST", "/v1/chat/stream", data=data,
                          headers={"Content-Type": "application/json", "Accept": "text/event-stream"})
        try:
            for event, payload in _iter_sse(resp):
                if event == "token":
                    yield json.loads(payload)  # token is a JSON-encoded string
                elif event == "error":
                    raise AeroVaultError(502, "StreamError", _maybe_unquote(payload))
                elif event == "done":
                    if on_done:
                        try:
                            on_done(ChatResponse.from_json(json.loads(payload)))
                        except Exception:  # noqa: BLE001 - callback must not break iteration
                            pass
                    return
        finally:
            resp.close()

    def agent(self, query: str) -> t.Dict[str, t.Any]:
        """Run the tool-calling agent loop (POST /v1/agent)."""
        return self._request_json("POST", "/v1/agent", json_body={"query": query}) or {}

    def lineage(self, object_id: int, limit: int = 0) -> t.Dict[str, t.Any]:
        """AI consumption history for an object (GET /v1/lineage/objects/<id>)."""
        return self._request_json(
            "GET", f"/v1/lineage/objects/{object_id}", params={"limit": limit or None}
        )

    # ---- ops -------------------------------------------------------------

    def usage(self) -> t.Dict[str, t.Any]:
        """Current tenant usage (GET /v1/usage)."""
        return self._request_json("GET", "/v1/usage")

    def health(self) -> bool:
        """True when the server's liveness probe returns 200."""
        try:
            with self._open("GET", "/healthz"):
                return True
        except AeroVaultError:
            return False

    # ---- admin -------

    def add_key(self, label: str, scopes: t.Optional[t.List[str]] = None,
                expires_at: t.Optional[str] = None) -> t.Dict[str, t.Any]:
        """Add an API key (POST /v1/admin/keys)."""
        body: t.Dict[str, t.Any] = {"label": label}
        if scopes: body["scopes"] = scopes
        if expires_at: body["expires_at"] = expires_at
        return self._request_json("POST", "/v1/admin/keys", json_body=body) or {}

    def list_keys(self) -> t.List[t.Any]:
        """List API keys (GET /v1/admin/keys)."""
        return (self._request_json("GET", "/v1/admin/keys") or {}).get("keys", [])

    def revoke_key(self, token: str) -> None:
        """Revoke an API key (DELETE /v1/admin/keys/{token})."""
        self._request_json("DELETE", f"/v1/admin/keys/{token}")

    def issue_jwt(self, tenant: str, scopes: t.Optional[t.List[str]] = None,
                  expires_in: int = 3600) -> t.Dict[str, t.Any]:
        """Issue a JWT (POST /v1/admin/jwt)."""
        body: t.Dict[str, t.Any] = {"tenant": tenant, "expires_in": expires_in}
        if scopes: body["scopes"] = scopes
        return self._request_json("POST", "/v1/admin/jwt", json_body=body) or {}

    def list_webhook_failures(self) -> t.List[t.Any]:
        """List webhook delivery failures (GET /v1/admin/webhook-failures)."""
        return (self._request_json("GET", "/v1/admin/webhook-failures") or {}).get("failures", [])

    def list_jobs(self) -> t.Dict[str, t.Any]:
        """List background jobs (GET /v1/admin/jobs)."""
        return self._request_json("GET", "/v1/admin/jobs") or {}

    def retry_job(self, job_id: int) -> t.Dict[str, t.Any]:
        """Retry a failed job (POST /v1/admin/jobs/{id}/retry)."""
        return self._request_json("POST", f"/v1/admin/jobs/{job_id}/retry") or {}

    def create_tenant(self, tenant_id: str, display_name: str = "",
                      max_bytes: int = 0, max_objects: int = 0) -> t.Dict[str, t.Any]:
        """Create a tenant (POST /v1/admin/tenants)."""
        body: t.Dict[str, t.Any] = {"tenant_id": tenant_id}
        if display_name: body["display_name"] = display_name
        if max_bytes: body["max_bytes"] = max_bytes
        if max_objects: body["max_objects"] = max_objects
        return self._request_json("POST", "/v1/admin/tenants", json_body=body) or {}

    def list_tenants(self) -> t.List[t.Any]:
        """List tenants (GET /v1/admin/tenants)."""
        return (self._request_json("GET", "/v1/admin/tenants") or {}).get("tenants", [])

    def delete_tenant(self, tenant_id: str) -> None:
        """Delete a tenant (DELETE /v1/admin/tenants/{tenant})."""
        self._request_json("DELETE", f"/v1/admin/tenants/{tenant_id}")

    def set_tenant_status(self, tenant_id: str, status: str) -> t.Dict[str, t.Any]:
        """Set tenant status to 'active' or 'disabled' (PUT /v1/admin/tenants/{tenant}/status)."""
        return self._request_json("PUT", f"/v1/admin/tenants/{tenant_id}/status",
                                  json_body={"status": status}) or {}

    def list_audit(self, limit: int = 50, before: t.Optional[str] = None) -> t.List[t.Any]:
        """List audit log entries (GET /v1/admin/audit)."""
        params: t.Dict[str, t.Any] = {"limit": limit}
        if before: params["before"] = before
        return (self._request_json("GET", "/v1/admin/audit", params=params) or {}).get("entries", [])

    def set_quota(self, tenant_id: str, max_bytes: int = 0, max_objects: int = 0) -> t.Dict[str, t.Any]:
        """Set tenant quota (PUT /v1/admin/tenants/{tenant}/quota)."""
        body: t.Dict[str, t.Any] = {}
        if max_bytes: body["max_bytes"] = max_bytes
        if max_objects: body["max_objects"] = max_objects
        return self._request_json("PUT", f"/v1/admin/tenants/{tenant_id}/quota", json_body=body) or {}

    def set_budget(self, tenant_id: str, daily_usd: float) -> t.Dict[str, t.Any]:
        """Set per-tenant AI daily budget (PUT /v1/admin/tenants/{tenant}/budget)."""
        return self._request_json("PUT", f"/v1/admin/tenants/{tenant_id}/budget",
                                  json_body={"daily_budget_usd": daily_usd}) or {}


# ---- helpers ------------------------------------------------------------


def _escape_key(key: str) -> str:
    """Percent-encode a key's path segments while preserving ``/`` separators."""
    return urllib.parse.quote(key.lstrip("/"), safe="/")


def _maybe_unquote(s: str) -> str:
    s = s.strip()
    if len(s) >= 2 and s[0] == '"' and s[-1] == '"':
        try:
            return json.loads(s)
        except Exception:  # noqa: BLE001
            return s[1:-1]
    return s


def _iter_sse(resp: t.Iterable[bytes]) -> t.Iterator[t.Tuple[str, str]]:
    """Parse an SSE byte stream into (event, data) pairs.

    Frames are separated by a blank line; ``data:`` lines accumulate (joined by
    newline), and an ``event:`` line names the frame (default ``message``).
    """
    event = "message"
    data_lines: t.List[str] = []
    for raw in resp:
        line = raw.decode("utf-8", "replace").rstrip("\n").rstrip("\r")
        if line == "":
            if data_lines:
                yield event, "\n".join(data_lines)
            event, data_lines = "message", []
            continue
        if line.startswith(":"):  # comment / keepalive
            continue
        if line.startswith("event:"):
            event = line[len("event:"):].strip()
        elif line.startswith("data:"):
            data_lines.append(line[len("data:"):].lstrip())
    if data_lines:
        yield event, "\n".join(data_lines)


# ---- tiny CLI for smoke testing -----------------------------------------


def _main(argv: t.Optional[t.List[str]] = None) -> int:
    import argparse
    import sys

    p = argparse.ArgumentParser(prog="aero_vault", description="aero-vault CLI / SDK smoke test")
    p.add_argument("--url", default=os.environ.get("AERO_VAULT_URL", "http://localhost:8080"))
    p.add_argument("--token", default=os.environ.get("AERO_VAULT_TOKEN"))
    p.add_argument("--tenant", default=os.environ.get("AERO_VAULT_TENANT", "default"))
    sub = p.add_subparsers(dest="cmd", required=True)

    sub.add_parser("ping")
    sp = sub.add_parser("put"); sp.add_argument("key"); sp.add_argument("path")
    sp = sub.add_parser("get"); sp.add_argument("key")
    sp = sub.add_parser("ls"); sp.add_argument("prefix", nargs="?", default="")
    sp = sub.add_parser("rm"); sp.add_argument("key"); sp.add_argument("--hard", action="store_true")
    sp = sub.add_parser("search"); sp.add_argument("query"); sp.add_argument("-k", type=int, default=5); sp.add_argument("--mode")
    sp = sub.add_parser("chat"); sp.add_argument("query"); sp.add_argument("--stream", action="store_true")

    args = p.parse_args(argv)
    av = Client(args.url, token=args.token, tenant=args.tenant)

    if args.cmd == "ping":
        print("ok" if av.health() else "unreachable")
    elif args.cmd == "put":
        print(av.upload_file(args.key, args.path))
    elif args.cmd == "get":
        sys.stdout.buffer.write(av.get(args.key))
    elif args.cmd == "ls":
        for o in av.iter_objects(prefix=args.prefix or None):
            print(f"{o.size:>12}  {o.key}")
    elif args.cmd == "rm":
        av.delete(args.key, hard=args.hard)
        print("deleted")
    elif args.cmd == "search":
        for h in av.search(args.query, k=args.k, mode=args.mode):
            print(f"{h.score:.4f}  {h.object_key}#{h.seq}  {h.chunk[:80]!r}")
    elif args.cmd == "chat":
        if args.stream:
            for tok in av.chat_stream(args.query):
                print(tok, end="", flush=True)
            print()
        else:
            print(av.chat(args.query).answer)
    return 0


if __name__ == "__main__":  # pragma: no cover
    raise SystemExit(_main())
