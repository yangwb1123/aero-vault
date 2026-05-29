# @aero-vault/sdk — JavaScript / TypeScript SDK

A zero-dependency client for the **aero-vault** AI-native file platform. Built
entirely on the Node.js 18+ global `fetch` and standard Web APIs, so it runs in
Node, Deno, Bun, and modern browsers with no transitive packages. TypeScript
types are bundled.

## Install

```bash
npm install @aero-vault/sdk
# or: pnpm add @aero-vault/sdk  /  yarn add @aero-vault/sdk
```

Or drop the two files (`aero-vault.js`, `aero-vault.d.ts`) straight into your
project — there are no dependencies to install.

Requires **Node.js >= 18** (for the global `fetch`). On older runtimes, pass a
`fetch` implementation via the constructor options.

## Quickstart

```js
import { Client } from "@aero-vault/sdk";

const av = new Client("http://localhost:8080", { token: "prod-rw", tenant: "acme" });

// --- files ---
await av.upload("docs/readme.txt", "hello world", { contentType: "text/plain" });
const bytes = await av.get("docs/readme.txt");      // Uint8Array
const text = await av.getText("docs/readme.txt");   // "hello world"
const meta = await av.stat("docs/readme.txt");      // { size, etag, content_type, ... }

for await (const obj of av.iterObjects({ prefix: "docs/" })) {
  console.log(obj.key, obj.size);                   // auto-paginates
}

await av.delete("docs/readme.txt");                 // soft delete; { hard: true } wipes bytes

// --- AI ---
const hits = await av.search("vector database", { k: 5, mode: "hybrid" });
for (const h of hits) console.log(h.score, h.object_key, h.chunk.slice(0, 60));

const reply = await av.chat("what is in the docs?");
console.log(reply.answer);
for (const c of reply.citations) console.log(" -", c.object_key);

for await (const token of av.chatStream("summarize everything")) {
  process.stdout.write(token);                      // streamed tokens (SSE)
}
```

## Upload & download

`upload(key, data, { contentType, metadata })` accepts a string, `Uint8Array`,
`ArrayBuffer`, `ArrayBufferView`, `Blob`, or `ReadableStream`. When you omit
`contentType` it is inferred from the key's extension (falling back to
`application/octet-stream`) — a Content-Type is always sent so the server can
index the object for search.

```js
await av.upload("imgs/logo.png", pngBytes);                  // -> image/png inferred
await av.upload("data.json", JSON.stringify(obj), {
  metadata: { team: "research", source: "etl" },            // -> X-Meta-team, X-Meta-source
});

const bytes = await av.get("imgs/logo.png");                 // Uint8Array
const text = await av.getText("data.json");                  // string (UTF-8)
const old = await av.get("data.json", { version: "v3" });    // a specific version
```

### Ranged / conditional / streaming reads

`getResponse` returns the raw `Response` so you can stream the body or do ranged
and conditional reads. A `304 Not Modified` or `206 Partial Content` is returned
without throwing.

```js
const r = await av.getResponse("video.mp4", { range: "bytes=0-1023" });
console.log(r.status); // 206
const chunk = new Uint8Array(await r.arrayBuffer());

const cond = await av.getResponse("data.json", { ifNoneMatch: '"abc123"' });
if (cond.status === 304) console.log("unchanged");

// stream a large object without buffering it all
const resp = await av.getResponse("big.bin");
for await (const part of resp.body) { /* part is a Uint8Array */ }
```

## Listing

```js
// one page
const page = await av.list({ prefix: "docs/", limit: 100 });
console.log(page.objects, page.next_marker, page.has_more);

// every object under a prefix (handles markers for you)
for await (const obj of av.iterObjects({ prefix: "docs/" })) {
  console.log(obj.key);
}
```

## Search & chat

```js
// mode: "vector" | "bm25" | "hybrid"
const hits = await av.search("how do refunds work?", { k: 8, mode: "hybrid", bucket: "kb" });

const reply = await av.chat("summarize the refund policy", { k: 6, temperature: 0.2 });
console.log(reply.answer, reply.model, reply.citations);
```

### Streaming chat (SSE)

`chatStream` is an async generator that yields answer tokens as they arrive.
Pass `onDone` to receive the final `ChatResponse` (answer + citations), and an
`AbortSignal` to cancel early.

```js
let final;
const ac = new AbortController();
for await (const token of av.chatStream("write a long summary", {
  signal: ac.signal,
  onDone: (resp) => { final = resp; },
})) {
  process.stdout.write(token);
}
console.log("\ncitations:", final?.citations);
```

### Agent

```js
const out = await av.agent("find the largest file and tell me its name");
console.log(out.answer, out.steps);
```

## Tags, versions, ACL, thumbnails, presign

```js
await av.putTags("docs/a.txt", { team: "research" });
const tags = await av.getTags("docs/a.txt");
await av.deleteTags("docs/a.txt");

const versions = await av.listVersions("docs/a.txt");

await av.setAcl("docs/a.txt", "public-read"); // private | public-read | public-read-write | authenticated-read
const { acl } = await av.getAcl("docs/a.txt");

const jpeg = await av.thumbnail("imgs/photo.jpg", { w: 256, h: 256 }); // Uint8Array (image/jpeg)

const { url } = await av.presign("docs/a.txt", "get", 900); // presigned URL, 15 min
```

## Multipart uploads

For large objects, upload in parts:

```js
const mp = await av.createMultipartUpload("big.bin", { contentType: "application/octet-stream" });
await av.uploadPart(mp.upload_id, 1, part1Bytes);
await av.uploadPart(mp.upload_id, 2, part2Bytes);
const obj = await av.completeMultipartUpload(mp.upload_id);
// or: await av.abortMultipartUpload(mp.upload_id);
```

## Usage & health

```js
const u = await av.usage();   // { used_bytes, used_objects, max_bytes, max_objects, ... }
const up = await av.health(); // true when /healthz returns 200
```

## Authentication & tenancy

| Concern              | How                                                                 |
| -------------------- | ------------------------------------------------------------------- |
| API key (default)    | `new Client(url, { token })` → `Authorization: Bearer <token>`      |
| `X-Api-Key` style    | `new Client(url, { token, apiKeyHeader: true })` → `X-Api-Key`      |
| JWT                  | same as API key — pass the JWT as `token`                           |
| Tenant               | `new Client(url, { tenant })` → `X-Aero-Tenant: <tenant>`           |
| Env vars (Node)      | `AERO_VAULT_URL`, `AERO_VAULT_TOKEN`, `AERO_VAULT_TENANT`            |
| Custom fetch / proxy | `new Client(url, { fetch: myFetch })`                               |
| Timeout              | `new Client(url, { timeout: 60000 })` ms (default 30000; `0` = off) |

When `baseUrl`, `token`, or `tenant` are omitted, the constructor reads the
corresponding `AERO_VAULT_*` environment variable (when a `process.env` exists),
then falls back to `http://localhost:8080` / no token / `"default"`.

```js
// reads AERO_VAULT_URL / AERO_VAULT_TOKEN / AERO_VAULT_TENANT
const av = new Client();
```

## Error handling

Every non-2xx response rejects with an `AeroVaultError` carrying the platform's
error envelope (`{ status, code, message, requestId }`). Network failures and
timeouts surface as `AeroVaultError` with `status === 0` and code
`NetworkError` / `Timeout`.

```js
import { AeroVaultError } from "@aero-vault/sdk";

try {
  await av.get("missing.txt");
} catch (e) {
  if (e instanceof AeroVaultError && e.status === 404) {
    console.log("not found", e.requestId);
  } else {
    throw e;
  }
}
```

## TypeScript

Types ship with the package; no `@types` install needed.

```ts
import { Client, type AeroObject, type SearchHit, type ChatResponse } from "@aero-vault/sdk";

const av = new Client(process.env.AERO_VAULT_URL, { token: process.env.AERO_VAULT_TOKEN });
const obj: AeroObject = await av.upload("a.txt", "hi");
const hits: SearchHit[] = await av.search("query", { mode: "hybrid" });
const reply: ChatResponse = await av.chat("question");
```

## API surface

`upload` · `get` · `getText` · `getResponse` · `stat` · `exists` · `list` ·
`iterObjects` · `delete` · `presign` · `thumbnail` · `getTags` · `putTags` ·
`deleteTags` · `listVersions` · `getAcl` · `setAcl` · `createMultipartUpload` ·
`uploadPart` · `completeMultipartUpload` · `abortMultipartUpload` · `search` ·
`chat` · `chatStream` · `agent` · `usage` · `health`.

## License

MIT
