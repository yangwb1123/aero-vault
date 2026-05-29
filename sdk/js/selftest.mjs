// Offline self-test for @aero-vault/sdk — no server required.
//
// Run:  node selftest.mjs   (from sdk/js/)
//
// The fetch layer is stubbed so we assert exactly what request the client
// builds (method, URL, headers, body) and how it decodes responses. Covers:
// Content-Type inference, JSON bodies, key escaping, error mapping, env
// fallback, list auto-pagination, and SSE parsing (multi-line frames,
// JSON-encoded token strings, and arbitrary network chunk boundaries).

import assert from "node:assert/strict";
import {
  Client,
  AeroVaultError,
  escapeKey,
  guessContentType,
  iterSSE,
  VERSION,
} from "./aero-vault.js";

let passed = 0;
const failures = [];

async function test(name, fn) {
  try {
    await fn();
    passed++;
    console.log(`  ok  ${name}`);
  } catch (e) {
    failures.push({ name, e });
    console.log(`FAIL  ${name}\n      ${e && e.stack ? e.stack : e}`);
  }
}

// ---- fetch stub ---------------------------------------------------------

/**
 * Build a stub fetch that records every call and returns a queued Response.
 * `responder` receives ({url, init}) and returns a Response (or its parts).
 */
function stubFetch(responder) {
  const calls = [];
  const fn = async (url, init) => {
    const call = { url: String(url), init, headers: headerObj(init && init.headers) };
    calls.push(call);
    const r = responder(call);
    return r instanceof Response ? r : makeResponse(r || {});
  };
  fn.calls = calls;
  return fn;
}

function headerObj(h) {
  const out = {};
  if (!h) return out;
  if (typeof h.forEach === "function" && !Array.isArray(h)) {
    h.forEach((v, k) => (out[k] = v));
  } else {
    for (const [k, v] of Object.entries(h)) out[k] = v;
  }
  return out;
}

function makeResponse({ status = 200, body = "", headers = {} } = {}) {
  // Headers must be set explicitly; the WHATWG Response won't infer them.
  return new Response(status === 204 || status === 304 ? null : body, { status, headers });
}

/** A ReadableStream that emits the given Uint8Array/string chunks in order. */
function streamOf(chunks) {
  const enc = new TextEncoder();
  const parts = chunks.map((c) => (typeof c === "string" ? enc.encode(c) : c));
  let i = 0;
  return new ReadableStream({
    pull(controller) {
      if (i < parts.length) controller.enqueue(parts[i++]);
      else controller.close();
    },
  });
}

function newClient(responder, opts = {}) {
  const fetch = stubFetch(responder);
  const client = new Client("http://test", { fetch, ...opts });
  return { client, fetch };
}

// ---- pure helpers -------------------------------------------------------

await test("escapeKey preserves slashes, encodes segments, strips leading /", () => {
  assert.equal(escapeKey("/a/b c.txt"), "a/b%20c.txt");
  assert.equal(escapeKey("docs/a.txt"), "docs/a.txt");
  assert.equal(escapeKey("weird key/with#hash?.txt"), "weird%20key/with%23hash%3F.txt");
});

await test("guessContentType infers from extension, null when unknown", () => {
  assert.equal(guessContentType("a.txt"), "text/plain");
  assert.equal(guessContentType("photo.JPG"), "image/jpeg");
  assert.equal(guessContentType("data.json"), "application/json");
  assert.equal(guessContentType("weird.xyzzy"), null);
  assert.equal(guessContentType("noext"), null);
});

await test("VERSION is exported", () => {
  assert.equal(typeof VERSION, "string");
  assert.ok(VERSION.length > 0);
});

// ---- upload: content-type inference + raw body + metadata ---------------

await test("upload infers Content-Type from .txt and sends raw body", async () => {
  const { client, fetch } = newClient(() => ({ body: JSON.stringify({ key: "a.txt", size: 3 }) }));
  const obj = await client.upload("docs/a.txt", "abc");
  const c = fetch.calls[0];
  assert.equal(c.init.method, "PUT");
  assert.equal(c.url, "http://test/v1/files/docs/a.txt");
  assert.equal(c.init.body, "abc"); // raw string body, not form-encoded
  assert.equal(c.headers["Content-Type"], "text/plain");
  assert.equal(obj.key, "a.txt");
  assert.equal(obj.size, 3);
});

await test("upload honors explicit Content-Type and X-Meta-* headers", async () => {
  const { client, fetch } = newClient(() => ({ body: "{}" }));
  await client.upload("blob.bin", new Uint8Array([0, 1]), {
    contentType: "application/pdf",
    metadata: { team: "x", project: "y" },
  });
  const c = fetch.calls[0];
  assert.equal(c.headers["Content-Type"], "application/pdf");
  assert.equal(c.headers["X-Meta-team"], "x");
  assert.equal(c.headers["X-Meta-project"], "y");
});

await test("upload falls back to octet-stream for unknown extension", async () => {
  const { client, fetch } = newClient(() => ({ body: "{}" }));
  await client.upload("weird.xyzzy", "q");
  assert.equal(fetch.calls[0].headers["Content-Type"], "application/octet-stream");
});

await test("upload accepts ArrayBuffer and ArrayBufferView bodies", async () => {
  const { client, fetch } = newClient(() => ({ body: "{}" }));
  const view = new Uint8Array([1, 2, 3, 4]);
  await client.upload("a.bin", view.buffer);
  assert.ok(fetch.calls[0].init.body instanceof Uint8Array);
});

// ---- get / getText / stat ----------------------------------------------

await test("get returns a Uint8Array of the body bytes", async () => {
  const { client } = newClient(() => ({ body: "hello world" }));
  const bytes = await client.get("docs/a.txt");
  assert.ok(bytes instanceof Uint8Array);
  assert.equal(new TextDecoder().decode(bytes), "hello world");
});

await test("get passes ?version= when provided", async () => {
  const { client, fetch } = newClient(() => ({ body: "x" }));
  await client.get("a.txt", { version: "v7" });
  assert.equal(fetch.calls[0].url, "http://test/v1/files/a.txt?version=v7");
});

await test("getText returns a decoded string", async () => {
  const { client } = newClient(() => ({ body: "héllo" }));
  assert.equal(await client.getText("a.txt"), "héllo");
});

await test("stat derives metadata from headers and strips ETag quotes", async () => {
  const { client } = newClient(() => ({
    status: 200,
    headers: { "Content-Length": "42", ETag: '"abc123"', "Content-Type": "text/plain" },
  }));
  const obj = await client.stat("a.txt");
  assert.equal(obj.size, 42);
  assert.equal(obj.etag, "abc123");
  assert.equal(obj.content_type, "text/plain");
  assert.equal(obj.key, "a.txt");
});

// ---- list / iterObjects -------------------------------------------------

await test("list returns a page with objects/next_marker/has_more", async () => {
  const { client, fetch } = newClient(() => ({
    body: JSON.stringify({ objects: [{ key: "a" }, { key: "b" }], next_marker: "m", has_more: true }),
  }));
  const page = await client.list({ prefix: "docs/", limit: 2 });
  assert.equal(page.objects.length, 2);
  assert.equal(page.next_marker, "m");
  assert.equal(page.has_more, true);
  assert.match(fetch.calls[0].url, /prefix=docs%2F/);
  assert.match(fetch.calls[0].url, /limit=2/);
});

await test("iterObjects auto-paginates across markers then stops", async () => {
  let n = 0;
  const { client, fetch } = newClient(() => {
    n++;
    if (n === 1) return { body: JSON.stringify({ objects: [{ key: "a" }], next_marker: "m1", has_more: true }) };
    if (n === 2) return { body: JSON.stringify({ objects: [{ key: "b" }], next_marker: "m2", has_more: true }) };
    return { body: JSON.stringify({ objects: [{ key: "c" }], has_more: false }) };
  });
  const keys = [];
  for await (const o of client.iterObjects({ prefix: "p/" })) keys.push(o.key);
  assert.deepEqual(keys, ["a", "b", "c"]);
  assert.equal(fetch.calls.length, 3);
  assert.match(fetch.calls[1].url, /marker=m1/);
  assert.match(fetch.calls[2].url, /marker=m2/);
});

// ---- delete / presign / thumbnail / acl ---------------------------------

await test("delete sends hard=1 only when requested", async () => {
  const { client, fetch } = newClient(() => ({ status: 204 }));
  await client.delete("a.txt");
  assert.equal(fetch.calls[0].init.method, "DELETE");
  assert.ok(!/hard=/.test(fetch.calls[0].url));
  await client.delete("a.txt", { hard: true });
  assert.match(fetch.calls[1].url, /hard=1/);
});

await test("presign posts op + expires query params", async () => {
  const { client, fetch } = newClient(() => ({ body: JSON.stringify({ url: "http://signed" }) }));
  const out = await client.presign("a.txt", "put", 120);
  assert.equal(fetch.calls[0].init.method, "POST");
  assert.match(fetch.calls[0].url, /\/v1\/files\/a\.txt\/presign/);
  assert.match(fetch.calls[0].url, /op=put/);
  assert.match(fetch.calls[0].url, /expires=120/);
  assert.equal(out.url, "http://signed");
});

await test("thumbnail returns bytes and passes w/h", async () => {
  const { client, fetch } = newClient(() => ({ body: "JPEGDATA", headers: { "Content-Type": "image/jpeg" } }));
  const bytes = await client.thumbnail("img.png", { w: 64, h: 48 });
  assert.ok(bytes instanceof Uint8Array);
  assert.match(fetch.calls[0].url, /\/thumbnail/);
  assert.match(fetch.calls[0].url, /w=64/);
  assert.match(fetch.calls[0].url, /h=48/);
  assert.equal(fetch.calls[0].headers["Accept"], "image/jpeg");
});

await test("setAcl PUTs an {acl} JSON body", async () => {
  const { client, fetch } = newClient(() => ({ body: "{}" }));
  await client.setAcl("a.txt", "public-read");
  const c = fetch.calls[0];
  assert.equal(c.init.method, "PUT");
  assert.match(c.url, /\/v1\/files\/a\.txt\/acl/);
  assert.equal(c.headers["Content-Type"], "application/json");
  assert.deepEqual(JSON.parse(c.init.body), { acl: "public-read" });
});

await test("putTags PUTs the tag map as JSON", async () => {
  const { client, fetch } = newClient(() => ({ body: "{}" }));
  await client.putTags("a.txt", { team: "research" });
  const c = fetch.calls[0];
  assert.equal(c.init.method, "PUT");
  assert.deepEqual(JSON.parse(c.init.body), { team: "research" });
});

// ---- search / chat ------------------------------------------------------

await test("search posts JSON body and returns hits", async () => {
  const { client, fetch } = newClient(() => ({
    body: JSON.stringify({ query: "q", hits: [{ object_key: "k", score: 1 }] }),
  }));
  const hits = await client.search("q", { k: 2, mode: "hybrid" });
  const c = fetch.calls[0];
  assert.equal(c.init.method, "POST");
  assert.equal(c.url, "http://test/v1/search");
  assert.equal(c.headers["Content-Type"], "application/json");
  assert.deepEqual(JSON.parse(c.init.body), { query: "q", k: 2, mode: "hybrid" });
  assert.equal(hits[0].object_key, "k");
});

await test("chat parses answer/model/citations and omits undefined opts", async () => {
  const { client, fetch } = newClient(() => ({
    body: JSON.stringify({ answer: "hi", model: "m", citations: [{ object_key: "d" }] }),
  }));
  const r = await client.chat("q", { temperature: 0.2 });
  assert.equal(r.answer, "hi");
  assert.equal(r.model, "m");
  assert.equal(r.citations.length, 1);
  assert.deepEqual(JSON.parse(fetch.calls[0].init.body), { query: "q", temperature: 0.2 });
});

// ---- auth + tenant headers + env fallback -------------------------------

await test("auth Bearer + tenant headers are sent", async () => {
  const { client, fetch } = newClient(() => ({ body: "{}" }), { token: "secret", tenant: "acme" });
  await client.list();
  const h = fetch.calls[0].headers;
  assert.equal(h["Authorization"], "Bearer secret");
  assert.equal(h["X-Aero-Tenant"], "acme");
});

await test("apiKeyHeader sends X-Api-Key instead of Authorization", async () => {
  const { client, fetch } = newClient(() => ({ body: "{}" }), { token: "k", apiKeyHeader: true });
  await client.list();
  const h = fetch.calls[0].headers;
  assert.equal(h["X-Api-Key"], "k");
  assert.ok(!("Authorization" in h));
});

await test("env fallback reads AERO_VAULT_URL/TOKEN/TENANT", () => {
  const save = {
    url: process.env.AERO_VAULT_URL,
    token: process.env.AERO_VAULT_TOKEN,
    tenant: process.env.AERO_VAULT_TENANT,
  };
  try {
    process.env.AERO_VAULT_URL = "http://env-host:9000/";
    process.env.AERO_VAULT_TOKEN = "env-token";
    process.env.AERO_VAULT_TENANT = "env-tenant";
    const c = new Client(undefined, { fetch: () => Promise.resolve(new Response("{}")) });
    assert.equal(c.baseUrl, "http://env-host:9000"); // trailing slash trimmed
    assert.equal(c.token, "env-token");
    assert.equal(c.tenant, "env-tenant");
  } finally {
    for (const [k, v] of [
      ["AERO_VAULT_URL", save.url],
      ["AERO_VAULT_TOKEN", save.token],
      ["AERO_VAULT_TENANT", save.tenant],
    ]) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
  }
});

// ---- error mapping ------------------------------------------------------

await test("non-2xx maps the error envelope to AeroVaultError", async () => {
  const { client } = newClient(() => ({
    status: 404,
    body: JSON.stringify({ error: { code: "NotFound", message: "nope", request_id: "r1" } }),
  }));
  await assert.rejects(
    () => client.get("missing.txt"),
    (e) => {
      assert.ok(e instanceof AeroVaultError);
      assert.equal(e.status, 404);
      assert.equal(e.code, "NotFound");
      assert.equal(e.message, "nope");
      assert.equal(e.requestId, "r1");
      assert.match(String(e), /request_id=r1/);
      return true;
    }
  );
});

await test("non-JSON error body becomes the message", async () => {
  const { client } = newClient(() => ({ status: 500, body: "boom\n" }));
  await assert.rejects(
    () => client.list(),
    (e) => {
      assert.equal(e.status, 500);
      assert.equal(e.code, "HTTPError");
      assert.equal(e.message, "boom");
      return true;
    }
  );
});

await test("health returns false on error, true on 200", async () => {
  const okClient = newClient(() => ({ status: 200, body: "ok" })).client;
  assert.equal(await okClient.health(), true);
  const badClient = newClient(() => ({ status: 503, body: "down" })).client;
  assert.equal(await badClient.health(), false);
});

await test("exists returns false on 404, true on 200, rethrows others", async () => {
  const okClient = newClient(() => ({ status: 200, headers: { "Content-Length": "1" } })).client;
  assert.equal(await okClient.exists("a"), true);
  const missingClient = newClient(() => ({ status: 404, body: "{}" })).client;
  assert.equal(await missingClient.exists("a"), false);
  const errClient = newClient(() => ({ status: 500, body: "x" })).client;
  await assert.rejects(() => errClient.exists("a"));
});

// ---- SSE parsing (iterSSE) ----------------------------------------------

async function collectSSE(stream) {
  const frames = [];
  for await (const f of iterSSE(stream)) frames.push(f);
  return frames;
}

await test("iterSSE parses simple framed events", async () => {
  const frames = await collectSSE(
    streamOf(['event: token\ndata: "Hello"\n\n', 'event: token\ndata: " world"\n\n'])
  );
  assert.deepEqual(frames, [
    ["token", '"Hello"'],
    ["token", '" world"'],
  ]);
});

await test("iterSSE handles a frame split across chunk boundaries", async () => {
  // The frame is fragmented mid-line / mid-frame to mimic real network reads.
  const frames = await collectSSE(streamOf(["event: tok", "en\nda", 'ta: "Hi', ' there"', "\n\n"]));
  assert.deepEqual(frames, [["token", '"Hi there"']]);
});

await test("iterSSE accumulates multi-line data and handles CRLF + comments", async () => {
  const frames = await collectSSE(
    streamOf([": keep-alive\r\n", "event: note\r\n", "data: line1\r\n", "data: line2\r\n", "\r\n"])
  );
  assert.deepEqual(frames, [["note", "line1\nline2"]]);
});

await test("chatStream yields decoded token strings and fires onDone", async () => {
  // Token frames carry JSON-encoded strings; done frame carries full ChatResp.
  const sse =
    'event: token\ndata: "Hello"\n\n' +
    'event: token\ndata: " world"\n\n' +
    'event: done\ndata: {"answer":"Hello world","model":"m","citations":[]}\n\n';
  const fetch = stubFetch(() => new Response(streamOf([sse]), { status: 200 }));
  const client = new Client("http://test", { fetch });
  let done = null;
  const tokens = [];
  for await (const t of client.chatStream("q", { onDone: (r) => (done = r) })) tokens.push(t);
  assert.deepEqual(tokens, ["Hello", " world"]);
  assert.equal(done.answer, "Hello world");
  assert.equal(done.model, "m");
  // Body sent as JSON, Accept set to text/event-stream.
  assert.deepEqual(JSON.parse(fetch.calls[0].init.body), { query: "q" });
  assert.equal(fetch.calls[0].headers["Accept"], "text/event-stream");
});

await test("chatStream raises AeroVaultError on an error frame", async () => {
  const sse = 'event: error\ndata: "model exploded"\n\n';
  const fetch = stubFetch(() => new Response(streamOf([sse]), { status: 200 }));
  const client = new Client("http://test", { fetch });
  await assert.rejects(
    (async () => {
      // eslint-disable-next-line no-unused-vars
      for await (const _ of client.chatStream("q")) {
        /* drain */
      }
    })(),
    (e) => {
      assert.ok(e instanceof AeroVaultError);
      assert.equal(e.code, "StreamError");
      assert.equal(e.message, "model exploded");
      return true;
    }
  );
});

await test("chatStream tokens split across chunks still decode whole", async () => {
  const fetch = stubFetch(() =>
    new Response(streamOf(["event: tok", 'en\ndata: "par', 'tial"\n', "\n", "event: done\ndata: {}\n\n"]), {
      status: 200,
    })
  );
  const client = new Client("http://test", { fetch });
  const tokens = [];
  for await (const t of client.chatStream("q")) tokens.push(t);
  assert.deepEqual(tokens, ["partial"]);
});

// ---- multipart ----------------------------------------------------------

await test("multipart init/part/complete hit the right routes", async () => {
  let n = 0;
  const fetch = stubFetch((call) => {
    n++;
    if (call.url.endsWith("/v1/multipart"))
      return { body: JSON.stringify({ upload_id: "U1", key: "big.bin", bucket: "b" }) };
    return { body: JSON.stringify({ key: "big.bin", size: 10 }) };
  });
  const client = new Client("http://test", { fetch });
  const mp = await client.createMultipartUpload("big.bin");
  assert.equal(mp.upload_id, "U1");
  await client.uploadPart("U1", 1, new Uint8Array([1, 2, 3]));
  await client.completeMultipartUpload("U1");
  assert.equal(fetch.calls[0].url, "http://test/v1/multipart");
  assert.equal(fetch.calls[1].init.method, "PUT");
  assert.equal(fetch.calls[1].url, "http://test/v1/multipart/U1/parts/1");
  assert.equal(fetch.calls[2].url, "http://test/v1/multipart/U1/complete");
});

// ---- summary ------------------------------------------------------------

console.log(`\n${passed} passed, ${failures.length} failed`);
if (failures.length) process.exit(1);
