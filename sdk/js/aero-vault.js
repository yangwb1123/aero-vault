// @ts-check
/**
 * aero-vault — JavaScript/TypeScript client for the aero-vault AI-native file platform.
 *
 * Zero runtime dependencies: built on the Node.js 18+ global `fetch` and standard
 * Web APIs (URL, Headers, TextDecoder, ReadableStream). Works in Node, Deno, Bun,
 * and modern browsers (where `process.env` is simply absent).
 *
 * @example
 * import { Client } from "@aero-vault/sdk";
 *
 * const av = new Client("http://localhost:8080", { token: "prod-rw", tenant: "acme" });
 * await av.upload("docs/readme.txt", "hello world", { contentType: "text/plain" });
 * const bytes = await av.get("docs/readme.txt");          // Uint8Array
 * console.log(await av.getText("docs/readme.txt"));        // "hello world"
 * for await (const obj of av.iterObjects({ prefix: "docs/" })) {
 *   console.log(obj.key, obj.size);
 * }
 * console.log(await av.search("hello", { k: 5 }));
 * console.log((await av.chat("what does the readme say?")).answer);
 * for await (const tok of av.chatStream("summarize the docs")) {
 *   process.stdout.write(tok);
 * }
 *
 * @module @aero-vault/sdk
 */

export const VERSION = "0.4.0";

/**
 * A request body may be raw bytes, text, or a streamable Web body.
 * @typedef {string | Uint8Array | ArrayBuffer | ArrayBufferView | Blob | ReadableStream} Body
 */

/**
 * Stored object metadata, as returned by upload/list/stat.
 * @typedef {Object} AeroObject
 * @property {string} bucket
 * @property {string} key
 * @property {number} size
 * @property {string} etag
 * @property {string} content_type
 * @property {string} backend
 * @property {Record<string,string>} metadata
 * @property {Record<string,string>} tags
 * @property {string} created_at
 * @property {string} updated_at
 */

/**
 * One ranked result from /v1/search (also used as a chat citation).
 * @typedef {Object} SearchHit
 * @property {number} score
 * @property {string} chunk
 * @property {number} chunk_id
 * @property {number} object_id
 * @property {string} bucket
 * @property {string} object_key
 * @property {number} seq
 * @property {string} embed_model
 */

/**
 * Answer plus grounding citations from /v1/chat.
 * @typedef {Object} ChatResponse
 * @property {string} answer
 * @property {string} model
 * @property {SearchHit[]} citations
 */

/**
 * A single page of a list response from GET /v1/files.
 * @typedef {Object} ListPage
 * @property {AeroObject[]} objects
 * @property {string} [next_marker]
 * @property {boolean} [has_more]
 */

/**
 * Result of a multipart upload init (POST /v1/multipart).
 * @typedef {Object} MultipartUpload
 * @property {string} upload_id
 * @property {string} key
 * @property {string} bucket
 */

/**
 * Options accepted by the {@link Client} constructor.
 * @typedef {Object} ClientOptions
 * @property {string} [token] API key or JWT. Falls back to `AERO_VAULT_TOKEN`.
 * @property {string} [tenant] Value for `X-Aero-Tenant`. Falls back to `AERO_VAULT_TENANT`, then "default".
 * @property {boolean} [apiKeyHeader] Send the token as `X-Api-Key` instead of `Authorization: Bearer`.
 * @property {typeof fetch} [fetch] Custom fetch implementation (defaults to global `fetch`).
 * @property {number} [timeout] Per-request timeout in milliseconds (default 30000; 0 disables).
 */

/** Read an env var when a `process.env` exists (Node), else undefined. */
function env(name) {
  try {
    // eslint-disable-next-line no-undef
    if (typeof process !== "undefined" && process && process.env) {
      // eslint-disable-next-line no-undef
      return process.env[name];
    }
  } catch {
    /* no process (browser) */
  }
  return undefined;
}

/**
 * Raised when the server returns a non-2xx response.
 *
 * The platform's error envelope is `{"error":{"code","message","request_id"}}`;
 * those fields are surfaced on the error when present.
 */
export class AeroVaultError extends Error {
  /**
   * @param {number} status HTTP status code.
   * @param {string} [code] Machine-readable error code.
   * @param {string} [message] Human-readable message.
   * @param {string} [requestId] Server request id, for support correlation.
   */
  constructor(status, code = "", message = "", requestId = "") {
    const finalCode = code || "HTTPError";
    const finalMessage = message || `HTTP ${status}`;
    super(finalMessage);
    this.name = "AeroVaultError";
    /** @type {number} HTTP status code (0 for network/timeout errors). */
    this.status = status;
    /** @type {string} Machine-readable error code. */
    this.code = finalCode;
    // `message` (set by super) is the bare server message, matching the
    // error envelope's `message` field. The bracketed/request_id form is
    // produced by toString() below.
    /** @type {string} Server request id, for support correlation. */
    this.requestId = requestId;
  }

  /**
   * Human-readable rendering including the status, code, and request id:
   * `[404 NotFound] nope (request_id=r1)`.
   * @returns {string}
   */
  toString() {
    const suffix = this.requestId ? ` (request_id=${this.requestId})` : "";
    return `[${this.status} ${this.code}] ${this.message}${suffix}`;
  }
}

/**
 * HTTP client for an aero-vault server.
 */
export class Client {
  /**
   * @param {string} [baseUrl] Service root, e.g. `http://localhost:8080`.
   *   Falls back to `AERO_VAULT_URL`, then `http://localhost:8080`.
   * @param {ClientOptions} [options]
   */
  constructor(baseUrl, options = {}) {
    const url = baseUrl || env("AERO_VAULT_URL") || "http://localhost:8080";
    /** @type {string} */
    this.baseUrl = url.replace(/\/+$/, "");
    /** @type {string | null} */
    this.token = options.token ?? env("AERO_VAULT_TOKEN") ?? null;
    /** @type {string} */
    this.tenant = options.tenant ?? env("AERO_VAULT_TENANT") ?? "default";
    /** @type {boolean} */
    this.apiKeyHeader = options.apiKeyHeader ?? false;
    /** @type {number} */
    this.timeout = options.timeout ?? 30000;
    const f = options.fetch ?? (typeof fetch !== "undefined" ? fetch : undefined);
    if (typeof f !== "function") {
      throw new TypeError(
        "global fetch is not available; pass { fetch } in ClientOptions (Node >= 18 required)"
      );
    }
    // Bind to avoid "Illegal invocation" when the global fetch is detached.
    /** @type {typeof fetch} */
    this._fetch = (input, init) => f(input, init);
  }

  // ---- low-level HTTP -------------------------------------------------

  /**
   * Build the default header set, merged with `extra` (null/undefined values dropped).
   * @param {Record<string,string|undefined|null>} [extra]
   * @returns {Record<string,string>}
   */
  _headers(extra) {
    /** @type {Record<string,string>} */
    const h = { "X-Aero-Tenant": this.tenant, Accept: "application/json" };
    if (this.token) {
      if (this.apiKeyHeader) h["X-Api-Key"] = this.token;
      else h["Authorization"] = "Bearer " + this.token;
    }
    if (extra) {
      for (const [k, v] of Object.entries(extra)) {
        if (v != null) h[k] = String(v);
      }
    }
    return h;
  }

  /**
   * Build a full URL with query params (null/undefined/"" values dropped).
   * @param {string} path
   * @param {Record<string, string|number|boolean|undefined|null>} [params]
   * @returns {string}
   */
  _url(path, params) {
    const u = new URL(this.baseUrl + path);
    if (params) {
      for (const [k, v] of Object.entries(params)) {
        if (v !== undefined && v !== null && v !== "") u.searchParams.set(k, String(v));
      }
    }
    return u.toString();
  }

  /**
   * Perform a raw fetch, applying headers, timeout, and error mapping.
   * On a non-2xx status this throws {@link AeroVaultError}.
   * @param {string} method
   * @param {string} path
   * @param {{params?: Record<string, any>, body?: BodyInit | null, headers?: Record<string,string|undefined|null>, signal?: AbortSignal}} [opts]
   * @returns {Promise<Response>}
   */
  async _fetchRaw(method, path, opts = {}) {
    const { params, body, headers, signal } = opts;
    let timer = null;
    let usedSignal = signal;
    // Apply a timeout only when the caller didn't pass their own signal.
    if (!usedSignal && this.timeout > 0 && typeof AbortController !== "undefined") {
      const ac = new AbortController();
      timer = setTimeout(() => ac.abort(), this.timeout);
      usedSignal = ac.signal;
    }
    let resp;
    try {
      /** @type {RequestInit & {duplex?: "half"}} */
      const init = {
        method,
        headers: this._headers(headers),
        body: body ?? null,
        signal: usedSignal,
      };
      // Node's undici requires this opt-in for a Web ReadableStream request
      // body. Keep it off for ordinary bodies so browser fetches remain
      // maximally portable.
      if (isReadableStreamBody(body)) init.duplex = "half";
      resp = await this._fetch(this._url(path, params), init);
    } catch (e) {
      if (timer) clearTimeout(timer);
      // Surface aborts/timeouts and network errors consistently.
      if (e && /** @type {any} */ (e).name === "AbortError") {
        throw new AeroVaultError(0, "Timeout", `request timed out after ${this.timeout}ms`);
      }
      throw new AeroVaultError(0, "NetworkError", String(/** @type {any} */ (e)?.message || e));
    }
    if (timer) clearTimeout(timer);
    if (!resp.ok) {
      throw await Client._toError(resp);
    }
    return resp;
  }

  /**
   * Map a non-2xx {@link Response} to an {@link AeroVaultError}, decoding the
   * platform error envelope `{"error":{...}}` when present.
   * @param {Response} resp
   * @returns {Promise<AeroVaultError>}
   */
  static async _toError(resp) {
    let code = "";
    let message = "";
    let requestId = "";
    let text = "";
    try {
      text = await resp.text();
    } catch {
      /* best-effort error body */
    }
    if (text) {
      try {
        const parsed = JSON.parse(text);
        const err =
          parsed && typeof parsed === "object" && parsed.error ? parsed.error : parsed;
        if (err && typeof err === "object") {
          code = err.code || "";
          message = err.message || "";
          requestId = err.request_id || "";
        } else {
          message = text.trim();
        }
      } catch {
        // non-JSON error body (e.g. plain http.Error text)
        message = text.trim();
      }
    }
    return new AeroVaultError(resp.status, code, message, requestId);
  }

  /**
   * Send a request and decode a JSON response (or null for an empty body).
   * Pass `json` to send a JSON body, or `body` to send a raw payload.
   * `body` takes precedence over `json`.
   * @param {string} method
   * @param {string} path
   * @param {{params?: Record<string, any>, json?: any, body?: BodyInit | null, headers?: Record<string,string|undefined|null>}} [opts]
   * @returns {Promise<any>}
   */
  async _requestJSON(method, path, opts = {}) {
    let { json, body, headers } = opts;
    const hdrs = { ...(headers || {}) };
    if (body == null && json !== undefined) {
      body = JSON.stringify(json);
      if (!("Content-Type" in hdrs)) hdrs["Content-Type"] = "application/json";
    }
    const resp = await this._fetchRaw(method, path, { params: opts.params, body, headers: hdrs });
    const text = await resp.text();
    if (!text) return null;
    return JSON.parse(text);
  }

  // ---- files ----------------------------------------------------------

  /**
   * Upload raw bytes to `key` (PUT /v1/files/<key>).
   *
   * Content-Type is always sent: when not given it is inferred from the key's
   * extension, falling back to `application/octet-stream`. (Omitting it can let
   * the server treat the body as form data, so it would never be indexed for
   * search.)
   * @param {string} key
   * @param {Body} data
   * @param {{contentType?: string, metadata?: Record<string,string>}} [opts]
   * @returns {Promise<AeroObject>}
   */
  async upload(key, data, opts = {}) {
    const body = coerceBody(data);
    const contentType = opts.contentType || guessContentType(key) || "application/octet-stream";
    /** @type {Record<string,string>} */
    const headers = { "Content-Type": contentType };
    for (const [mk, mv] of Object.entries(opts.metadata || {})) {
      headers["X-Meta-" + mk] = mv;
    }
    const out = await this._requestJSON("PUT", "/v1/files/" + escapeKey(key), { body, headers });
    return /** @type {AeroObject} */ (out || {});
  }

  /**
   * Download an object's bytes (GET /v1/files/<key>).
   * @param {string} key
   * @param {{version?: string}} [opts]
   * @returns {Promise<Uint8Array>}
   */
  async get(key, opts = {}) {
    const resp = await this._fetchRaw("GET", "/v1/files/" + escapeKey(key), {
      params: { version: opts.version },
    });
    const buf = await resp.arrayBuffer();
    return new Uint8Array(buf);
  }

  /**
   * Download an object and decode it as UTF-8 text.
   * @param {string} key
   * @param {{version?: string}} [opts]
   * @returns {Promise<string>}
   */
  async getText(key, opts = {}) {
    const resp = await this._fetchRaw("GET", "/v1/files/" + escapeKey(key), {
      params: { version: opts.version },
    });
    return resp.text();
  }

  /**
   * Download an object as a raw {@link Response} for streaming, ranged reads,
   * or conditional requests. Pass `range` (e.g. "bytes=0-1023") and/or
   * `ifNoneMatch` (an ETag). A 304/206 response is returned without throwing.
   * @param {string} key
   * @param {{version?: string, range?: string, ifNoneMatch?: string}} [opts]
   * @returns {Promise<Response>}
   */
  async getResponse(key, opts = {}) {
    /** @type {Record<string,string|undefined>} */
    const headers = {};
    if (opts.range) headers["Range"] = opts.range;
    if (opts.ifNoneMatch) headers["If-None-Match"] = opts.ifNoneMatch;
    // A conditional/ranged GET legitimately returns 304/206; don't treat as error.
    const resp = await this._fetch(
      this._url("/v1/files/" + escapeKey(key), { version: opts.version }),
      { method: "GET", headers: this._headers(headers) }
    );
    if (!resp.ok && resp.status !== 304 && resp.status !== 206) {
      throw await Client._toError(resp);
    }
    return resp;
  }

  /**
   * HEAD an object; returns metadata derived from response headers.
   * @param {string} key
   * @returns {Promise<AeroObject>}
   */
  async stat(key) {
    const resp = await this._fetchRaw("HEAD", "/v1/files/" + escapeKey(key));
    const h = resp.headers;
    const len = h.get("Content-Length");
    return {
      bucket: "",
      key,
      size: len ? parseInt(len, 10) || 0 : 0,
      etag: (h.get("ETag") || "").replace(/^"|"$/g, ""),
      content_type: h.get("Content-Type") || "",
      backend: "",
      metadata: {},
      tags: {},
      created_at: "",
      updated_at: h.get("Last-Modified") || "",
    };
  }

  /**
   * True if the object exists (404 → false).
   * @param {string} key
   * @returns {Promise<boolean>}
   */
  async exists(key) {
    try {
      await this.stat(key);
      return true;
    } catch (e) {
      if (e instanceof AeroVaultError && e.status === 404) return false;
      throw e;
    }
  }

  /**
   * List a single page of objects (GET /v1/files).
   * @param {{prefix?: string, marker?: string, limit?: number}} [opts]
   * @returns {Promise<ListPage>}
   */
  async list(opts = {}) {
    const out = await this._requestJSON("GET", "/v1/files", {
      params: { prefix: opts.prefix, marker: opts.marker, limit: opts.limit },
    });
    const page = out || {};
    return {
      objects: page.objects || [],
      next_marker: page.next_marker,
      has_more: page.has_more,
    };
  }

  /**
   * Auto-paginate over every object under `prefix`.
   * @param {{prefix?: string, pageSize?: number}} [opts]
   * @returns {AsyncGenerator<AeroObject, void, unknown>}
   */
  async *iterObjects(opts = {}) {
    /** @type {string | undefined} */
    let marker = undefined;
    const limit = opts.pageSize ?? 1000;
    for (;;) {
      const out = await this._requestJSON("GET", "/v1/files", {
        params: { prefix: opts.prefix, marker, limit },
      });
      const page = out || {};
      for (const o of page.objects || []) yield o;
      if (!page.has_more) return;
      marker = page.next_marker || undefined;
      if (!marker) return;
    }
  }

  /**
   * Delete an object (soft by default; `hard: true` removes the bytes).
   * @param {string} key
   * @param {{hard?: boolean}} [opts]
   * @returns {Promise<void>}
   */
  async delete(key, opts = {}) {
    await this._fetchRaw("DELETE", "/v1/files/" + escapeKey(key), {
      params: { hard: opts.hard ? "1" : undefined },
    });
  }

  /**
   * Create a presigned URL (op = `get` | `put`).
   * @param {string} key
   * @param {"get"|"put"} [op]
   * @param {number} [expires] Seconds until expiry.
   * @returns {Promise<Record<string, any>>}
   */
  async presign(key, op = "get", expires = 900) {
    return this._requestJSON("POST", "/v1/files/" + escapeKey(key) + "/presign", {
      params: { op, expires },
    });
  }

  /**
   * On-demand JPEG thumbnail of an image object (GET /v1/files/<key>/thumbnail).
   * @param {string} key
   * @param {{w?: number, h?: number}} [opts]
   * @returns {Promise<Uint8Array>}
   */
  async thumbnail(key, opts = {}) {
    const resp = await this._fetchRaw("GET", "/v1/files/" + escapeKey(key) + "/thumbnail", {
      params: { w: opts.w, h: opts.h },
      headers: { Accept: "image/jpeg" },
    });
    const buf = await resp.arrayBuffer();
    return new Uint8Array(buf);
  }

  // ---- tags / versions / acl -----------------------------------------

  /**
   * Get an object's tag map (GET /v1/files/<key>/tags).
   * @param {string} key
   * @returns {Promise<Record<string,string>>}
   */
  async getTags(key) {
    return (await this._requestJSON("GET", "/v1/files/" + escapeKey(key) + "/tags")) || {};
  }

  /**
   * Replace an object's tags (PUT /v1/files/<key>/tags).
   * @param {string} key
   * @param {Record<string,string>} tags
   * @returns {Promise<any>}
   */
  async putTags(key, tags) {
    return this._requestJSON("PUT", "/v1/files/" + escapeKey(key) + "/tags", {
      json: { ...tags },
    });
  }

  /**
   * Clear an object's tags (DELETE /v1/files/<key>/tags).
   * @param {string} key
   * @returns {Promise<void>}
   */
  async deleteTags(key) {
    await this._fetchRaw("DELETE", "/v1/files/" + escapeKey(key) + "/tags");
  }

  /**
   * List an object's versions (GET /v1/files/<key>/versions).
   * @param {string} key
   * @returns {Promise<any>}
   */
  async listVersions(key) {
    return this._requestJSON("GET", "/v1/files/" + escapeKey(key) + "/versions");
  }

  /**
   * Apply an object lock retaining the object for ``seconds`` from now.
   * POST /v1/files/<key>/lock
   * @param {string} key - object key
   * @param {number} seconds - retention duration in seconds
   * @returns {Promise<object>}
   */
  async lock(key, seconds) {
    return this._requestJSON("POST", "/v1/files/" + escapeKey(key) + "/lock", {
      json: { seconds },
    });
  }

  /**
   * Get an object's canned ACL (GET /v1/files/<key>/acl).
   * @param {string} key
   * @returns {Promise<{acl: string}>}
   */
  async getAcl(key) {
    return this._requestJSON("GET", "/v1/files/" + escapeKey(key) + "/acl");
  }

  /**
   * Set an object's canned ACL (PUT /v1/files/<key>/acl).
   * @param {string} key
   * @param {"private"|"public-read"|"public-read-write"|"authenticated-read"} acl
   * @returns {Promise<any>}
   */
  async setAcl(key, acl) {
    return this._requestJSON("PUT", "/v1/files/" + escapeKey(key) + "/acl", { json: { acl } });
  }

  /**
   * Get a bucket's canned ACL (GET /v1/buckets/<bucket>/acl).
   * @param {string} bucket
   * @returns {Promise<{acl: string}>}
   */
  async getBucketACL(bucket) {
    return this._requestJSON("GET", "/v1/buckets/" + encodeURIComponent(bucket) + "/acl");
  }

  /**
   * Set a bucket's canned ACL (PUT /v1/buckets/<bucket>/acl).
   * @param {string} bucket
   * @param {"private"|"public-read"|"public-read-write"|"authenticated-read"} acl
   * @returns {Promise<any>}
   */
  async setBucketACL(bucket, acl) {
    return this._requestJSON("PUT", "/v1/buckets/" + encodeURIComponent(bucket) + "/acl", {
      json: { acl },
    });
  }

  // ---- multipart uploads ----------------------------------------------

  /**
   * Initiate a multipart upload (POST /v1/multipart).
   * @param {string} key
   * @param {{contentType?: string}} [opts]
   * @returns {Promise<MultipartUpload>}
   */
  async createMultipartUpload(key, opts = {}) {
    const contentType = opts.contentType || guessContentType(key) || "application/octet-stream";
    return this._requestJSON("POST", "/v1/multipart", {
      json: { key, content_type: contentType },
    });
  }

  /**
   * Upload one part of a multipart upload (PUT /v1/multipart/<uploadID>/parts/<n>).
   * @param {string} uploadId
   * @param {number} partNumber 1-based part index.
   * @param {Body} data
   * @returns {Promise<any>}
   */
  async uploadPart(uploadId, partNumber, data) {
    return this._requestJSON(
      "PUT",
      "/v1/multipart/" +
        encodeURIComponent(uploadId) +
        "/parts/" +
        encodeURIComponent(String(partNumber)),
      { body: coerceBody(data), headers: { "Content-Type": "application/octet-stream" } }
    );
  }

  /**
   * Complete a multipart upload (POST /v1/multipart/<uploadID>/complete).
   * @param {string} uploadId
   * @returns {Promise<AeroObject>}
   */
  async completeMultipartUpload(uploadId) {
    const out = await this._requestJSON(
      "POST",
      "/v1/multipart/" + encodeURIComponent(uploadId) + "/complete",
      { json: {} }
    );
    return /** @type {AeroObject} */ (out || {});
  }

  /**
   * Abort a multipart upload (DELETE /v1/multipart/<uploadID>).
   * @param {string} uploadId
   * @returns {Promise<void>}
   */
  async abortMultipartUpload(uploadId) {
    await this._fetchRaw("DELETE", "/v1/multipart/" + encodeURIComponent(uploadId));
  }

  // ---- AI: search / chat / agent -------------------------------------

  /**
   * Semantic / lexical / hybrid search (POST /v1/search).
   * @param {string} query
   * @param {{k?: number, mode?: "vector"|"bm25"|"hybrid", bucket?: string}} [opts]
   * @returns {Promise<SearchHit[]>}
   */
  async search(query, opts = {}) {
    /** @type {Record<string, any>} */
    const body = { query, k: opts.k ?? 10 };
    if (opts.mode) body.mode = opts.mode;
    if (opts.bucket) body.bucket = opts.bucket;
    const out = (await this._requestJSON("POST", "/v1/search", { json: body })) || {};
    return out.hits || [];
  }

  /**
   * RAG chat with citations (POST /v1/chat).
   * @param {string} query
   * @param {{k?: number, mode?: "vector"|"bm25"|"hybrid", bucket?: string, temperature?: number, prior?: Array<Record<string,string>>}} [opts]
   * @returns {Promise<ChatResponse>}
   */
  async chat(query, opts = {}) {
    const body = buildChatBody(query, opts, true);
    const out = (await this._requestJSON("POST", "/v1/chat", { json: body })) || {};
    return {
      answer: out.answer || "",
      model: out.model || "",
      citations: out.citations || [],
    };
  }

  /**
   * Streaming RAG chat (POST /v1/chat/stream, Server-Sent Events).
   * Yields answer token strings as they arrive. When the stream finishes and
   * `onDone` is provided, it is invoked with the final {@link ChatResponse}.
   * @param {string} query
   * @param {{k?: number, mode?: "vector"|"bm25"|"hybrid", bucket?: string, onDone?: (resp: ChatResponse) => void, signal?: AbortSignal}} [opts]
   * @returns {AsyncGenerator<string, void, unknown>}
   */
  async *chatStream(query, opts = {}) {
    const body = buildChatBody(query, opts, false);
    const resp = await this._fetchRaw("POST", "/v1/chat/stream", {
      body: JSON.stringify(body),
      headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
      signal: opts.signal,
    });
    if (!resp.body) {
      throw new AeroVaultError(502, "StreamError", "response had no body to stream");
    }
    for await (const [event, payload] of iterSSE(resp.body)) {
      if (event === "token") {
        // Each token frame is a JSON-encoded string.
        yield /** @type {string} */ (JSON.parse(payload));
      } else if (event === "error") {
        throw parseStreamError(payload);
      } else if (event === "done") {
        if (opts.onDone) {
          try {
            const d = JSON.parse(payload);
            opts.onDone({
              answer: d.answer || "",
              model: d.model || "",
              citations: d.citations || [],
            });
          } catch {
            /* callback must not break iteration */
          }
        }
        return;
      }
    }
  }

  /**
   * Run the tool-calling agent loop (POST /v1/agent).
   * @param {string} query
   * @returns {Promise<{answer?: string, steps?: any, model?: string}>}
   */
  async agent(query) {
    return (await this._requestJSON("POST", "/v1/agent", { json: { query } })) || {};
  }

  /**
   * AI consumption history for an object (GET /v1/lineage/objects/<id>).
   * @param {number} objectId
   * @param {number} [limit] 0 lets the server pick its default.
   * @returns {Promise<{object_id: number, entries: any[]}>}
   */
  async lineage(objectId, limit = 0) {
    return this._requestJSON("GET", `/v1/lineage/objects/${encodeURIComponent(objectId)}`, {
      params: { limit: limit || undefined },
    });
  }

  // ---- ops -------------------------------------------------------------

  /**
   * Current tenant usage (GET /v1/usage).
   * @returns {Promise<{tenant?: string, used_bytes?: number, used_objects?: number, max_bytes?: number, max_objects?: number}>}
   */
  async usage() {
    return this._requestJSON("GET", "/v1/usage");
  }

  /**
   * True when the server's liveness probe returns 200 (GET /healthz).
   * @returns {Promise<boolean>}
   */
  async health() {
    try {
      await this._fetchRaw("GET", "/healthz");
      return true;
    } catch {
      return false;
    }
  }

  // ---- admin ----

  /**
   * Add an API key (POST /v1/admin/keys). The server requires `token`,
   * `tenant` and `scopes`; `tenant` is taken from this client's tenant.
   * @param {string} token
   * @param {string[]} scopes
   * @param {{label?: string, expires?: string}} [opts] `expires` is an optional RFC3339 timestamp.
   * @returns {Promise<{tenant?: string, scopes?: string[]}>}
   */
  async addKey(token, scopes, opts = {}) {
    return this._requestJSON("POST", "/v1/admin/keys", {
      json: { token, tenant: this.tenant, scopes, ...opts },
    });
  }

  /** List API keys. @returns {Promise<{keys?: Array}>} */
  async listKeys() {
    return this._requestJSON("GET", "/v1/admin/keys");
  }

  /** Revoke an API key. */
  async revokeKey(token) {
    return this._requestJSON("DELETE", `/v1/admin/keys/${encodeURIComponent(token)}`);
  }

  /** Issue a JWT. @returns {Promise<{token?: string}>} */
  async issueJWT(tenant, opts = {}) {
    return this._requestJSON("POST", "/v1/admin/jwt", {
      json: { tenant, ...opts },
    });
  }

  /** List webhook delivery failures. */
  async listWebhookFailures() {
    return this._requestJSON("GET", "/v1/admin/webhook-failures");
  }

  /** List background jobs. */
  async listJobs() {
    return this._requestJSON("GET", "/v1/admin/jobs");
  }

  /** Retry a failed job. */
  async retryJob(jobId) {
    return this._requestJSON("POST", `/v1/admin/jobs/${jobId}/retry`);
  }

  /** Create a tenant. */
  async createTenant(tenantId, opts = {}) {
    return this._requestJSON("POST", "/v1/admin/tenants", {
      json: { tenant_id: tenantId, ...opts },
    });
  }

  /** List tenants. @returns {Promise<{tenants?: Array}>} */
  async listTenants() {
    return this._requestJSON("GET", "/v1/admin/tenants");
  }

  /** Delete a tenant. */
  async deleteTenant(tenantId) {
    return this._requestJSON("DELETE", `/v1/admin/tenants/${encodeURIComponent(tenantId)}`);
  }

  /** Set tenant active/disabled status. */
  async setTenantStatus(tenantId, status) {
    return this._requestJSON("PUT", `/v1/admin/tenants/${encodeURIComponent(tenantId)}/status`, {
      json: { status },
    });
  }

  /** List audit log entries. */
  async listAudit(opts = {}) {
    return this._requestJSON("GET", "/v1/admin/audit", {
      params: { limit: opts.limit, before: opts.before },
    });
  }

  /** Set tenant storage quota. */
  async setQuota(tenantId, opts = {}) {
    return this._requestJSON("PUT", `/v1/admin/tenants/${encodeURIComponent(tenantId)}/quota`, {
      json: opts,
    });
  }

  /** Set per-tenant AI daily budget (USD). */
  async setBudget(tenantId, dailyUSD) {
    return this._requestJSON("PUT", `/v1/admin/tenants/${encodeURIComponent(tenantId)}/budget`, {
      json: { daily_budget_usd: dailyUSD },
    });
  }

  // ---- enterprise access / sharing / publishing ----

  /** Create a revocable object capability link. */
  async createShare(key, opts = {}) {
    return this._requestJSON("POST", "/v1/shares", {
      json: {
        bucket: opts.bucket || "default", key, name: opts.name || "",
        password: opts.password || "", allow_preview: opts.allowPreview ?? true,
        allow_download: opts.allowDownload ?? false, max_uses: opts.maxUses || 0,
        ttl_seconds: opts.ttlSeconds || 0,
      },
    });
  }

  /** List share records for one object. Raw share tokens are never returned. */
  async listShares(key, bucket = "default") {
    const out = await this._requestJSON("GET", "/v1/shares", { params: { bucket, key } });
    return out?.shares || [];
  }

  /** Revoke one share link. */
  async revokeShare(id) {
    return this._requestJSON("DELETE", `/v1/shares/${encodeURIComponent(id)}`);
  }

  /** Publish an image under a stable public slug. */
  async publishAsset(key, slug, opts = {}) {
    return this._requestJSON("POST", "/v1/assets", {
      json: {
        bucket: opts.bucket || "default", key, slug,
        cache_control: opts.cacheControl || "public, max-age=3600",
      },
    });
  }

  /** Remove a public image slug without deleting its source object. */
  async unpublishAsset(slug) {
    return this._requestJSON("DELETE", `/v1/assets/${escapeKey(slug)}`);
  }

  /** List published images for the active tenant. */
  async listAssets() {
    const out = await this._requestJSON("GET", "/v1/assets");
    return out?.assets || [];
  }

  /** Grant or deny one or more actions on a resource. */
  async putResourceACL(input) {
    return this._requestJSON("PUT", "/v1/access/acl", { json: input });
  }

  /** List ACL entries directly attached to a resource. */
  async listResourceACL(key = "", opts = {}) {
    const out = await this._requestJSON("GET", "/v1/access/acl", {
      params: { bucket: opts.bucket || "default", key, kind: opts.resourceKind || "object" },
    });
    return out?.entries || [];
  }

  /** Delete one resource ACL entry. */
  async deleteResourceACL(id) {
    return this._requestJSON("DELETE", `/v1/access/acl/${encodeURIComponent(id)}`);
  }

  /** Create a department (admin scope). */
  async createDepartment(name, parentId = "") {
    return this._requestJSON("POST", "/v1/admin/departments", {
      json: { name, parent_id: parentId },
    });
  }

  /** List departments for the active tenant (admin scope). */
  async listDepartments() {
    const out = await this._requestJSON("GET", "/v1/admin/departments");
    return out?.departments || [];
  }

  /** Get one department and its members (admin scope). */
  async getDepartment(id) {
    return this._requestJSON("GET", `/v1/admin/departments/${encodeURIComponent(id)}`);
  }

  /** Delete a department (admin scope). */
  async deleteDepartment(id) {
    return this._requestJSON("DELETE", `/v1/admin/departments/${encodeURIComponent(id)}`);
  }

  /** Add or update a department member (admin scope). */
  async putDepartmentMember(departmentId, subjectId, role = "member") {
    const path = `/v1/admin/departments/${encodeURIComponent(departmentId)}/members/${encodeURIComponent(subjectId)}`;
    return this._requestJSON("PUT", path, { json: { role } });
  }

  /** Remove a department member (admin scope). */
  async deleteDepartmentMember(departmentId, subjectId) {
    const path = `/v1/admin/departments/${encodeURIComponent(departmentId)}/members/${encodeURIComponent(subjectId)}`;
    return this._requestJSON("DELETE", path);
  }

  /** Download an authorized portable tar.gz backup as Uint8Array. */
  async exportArchive(opts = {}) {
    const response = await this._fetchRaw("GET", "/v1/exports/archive", {
      params: { bucket: opts.bucket || "default", prefix: opts.prefix || "" },
    });
    return new Uint8Array(await response.arrayBuffer());
  }
}

// ---- helpers ------------------------------------------------------------

/**
 * Percent-encode a key's path segments while preserving `/` separators.
 * Mirrors urllib.parse.quote(key, safe="/").
 * @param {string} key
 * @returns {string}
 */
export function escapeKey(key) {
  return key
    .replace(/^\/+/, "")
    .split("/")
    .map((seg) => encodeURIComponent(seg))
    .join("/");
}

/**
 * Coerce an SDK {@link Body} into something fetch accepts. Strings, Blobs and
 * ReadableStreams pass through; ArrayBuffer(View)s become a Uint8Array.
 * @param {Body} data
 * @returns {BodyInit}
 */
export function coerceBody(data) {
  if (data == null) {
    throw new TypeError("data must not be null/undefined");
  }
  if (typeof data === "string") return data;
  if (data instanceof Uint8Array) return data;
  if (data instanceof ArrayBuffer) return new Uint8Array(data);
  if (ArrayBuffer.isView(data)) {
    const v = /** @type {ArrayBufferView} */ (data);
    return new Uint8Array(v.buffer, v.byteOffset, v.byteLength);
  }
  // Blob, ReadableStream, etc. are valid BodyInit values.
  return /** @type {BodyInit} */ (data);
}

/**
 * Detect a Web ReadableStream without relying on a realm-specific
 * `instanceof` check (streams can come from an iframe or another runtime).
 * @param {unknown} body
 * @returns {boolean}
 */
function isReadableStreamBody(body) {
  return body != null && typeof body === "object" && typeof body.getReader === "function";
}

/**
 * Assemble the JSON body for chat/chatStream, omitting undefined options.
 * @param {string} query
 * @param {Record<string, any>} opts
 * @param {boolean} allowExtended Include temperature/prior (chat only).
 * @returns {Record<string, any>}
 */
function buildChatBody(query, opts, allowExtended) {
  /** @type {Record<string, any>} */
  const body = { query };
  if (opts.k !== undefined) body.k = opts.k;
  if (opts.mode !== undefined) body.mode = opts.mode;
  if (opts.bucket !== undefined) body.bucket = opts.bucket;
  if (allowExtended) {
    if (opts.temperature !== undefined) body.temperature = opts.temperature;
    if (opts.prior !== undefined) body.prior = opts.prior;
  }
  return body;
}

/**
 * If `s` is a JSON-encoded string ("...") decode it, else return it trimmed.
 * @param {string} s
 * @returns {string}
 */
function maybeUnquote(s) {
  const t = s.trim();
  if (t.length >= 2 && t[0] === '"' && t[t.length - 1] === '"') {
    try {
      return JSON.parse(t);
    } catch {
      return t.slice(1, -1);
    }
  }
  return t;
}

/**
 * Decode the structured `event: error` payload emitted by ChatStream.
 * Keep accepting the old JSON-string payload for compatibility with older
 * servers and test fixtures.
 * @param {string} payload
 * @returns {AeroVaultError}
 */
function parseStreamError(payload) {
  const fallback = maybeUnquote(payload);
  let code = "StreamError";
  let message = fallback;
  let status = 502;
  try {
    const parsed = JSON.parse(payload);
    if (typeof parsed === "string") {
      message = parsed;
    } else if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      if (typeof parsed.code === "string" && parsed.code) code = parsed.code;
      if (typeof parsed.message === "string") message = parsed.message;
      if (Number.isInteger(parsed.status) && parsed.status >= 400) status = parsed.status;
    }
  } catch {
    // Preserve the trimmed raw payload for malformed/legacy frames.
  }
  if (code === "BudgetExceeded" && status === 502) status = 402;
  return new AeroVaultError(status, code, message);
}

/**
 * Parse an SSE byte stream into [event, data] pairs.
 *
 * Frames are separated by a blank line; consecutive `data:` lines accumulate
 * (joined by newline) and an `event:` line names the frame (default "message").
 * Handles arbitrary chunk boundaries (a frame may span multiple network reads)
 * and both `\n` and `\r\n` line endings.
 * @param {ReadableStream<Uint8Array>} stream
 * @returns {AsyncGenerator<[string, string], void, unknown>}
 */
export async function* iterSSE(stream) {
  const decoder = new TextDecoder("utf-8");
  const reader = stream.getReader();
  let buffer = "";
  let event = "message";
  /** @type {string[]} */
  let dataLines = [];

  /** @returns {[string,string]|null} */
  const flush = () => {
    if (dataLines.length) {
      const frame = /** @type {[string,string]} */ ([event, dataLines.join("\n")]);
      event = "message";
      dataLines = [];
      return frame;
    }
    event = "message";
    dataLines = [];
    return null;
  };

  /**
   * Feed one logical line; returns a frame if the line closed one.
   * @param {string} line
   * @returns {[string,string]|null}
   */
  const onLine = (line) => {
    if (line === "") return flush();
    if (line.startsWith(":")) return null; // comment / keepalive
    if (line.startsWith("event:")) {
      event = line.slice("event:".length).trim();
      return null;
    }
    if (line.startsWith("data:")) {
      dataLines.push(line.slice("data:".length).replace(/^ /, ""));
      return null;
    }
    return null;
  };

  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let idx;
      // Split on \n; strip a trailing \r so \r\n endings work too.
      while ((idx = buffer.indexOf("\n")) !== -1) {
        let line = buffer.slice(0, idx);
        buffer = buffer.slice(idx + 1);
        if (line.endsWith("\r")) line = line.slice(0, -1);
        const frame = onLine(line);
        if (frame) yield frame;
      }
    }
    // Flush any trailing buffered line (stream ended without a final newline).
    buffer += decoder.decode();
    if (buffer.length) {
      if (buffer.endsWith("\r")) buffer = buffer.slice(0, -1);
      const frame = onLine(buffer);
      if (frame) yield frame;
    }
    const tail = flush();
    if (tail) yield tail;
  } finally {
    try {
      reader.releaseLock();
    } catch {
      /* ignore */
    }
  }
}

/**
 * Guess a Content-Type from a key's file extension. A small built-in table
 * covering common document/image/text/code types; returns null when unknown.
 * @param {string} key
 * @returns {string | null}
 */
export function guessContentType(key) {
  const m = /\.([A-Za-z0-9]+)$/.exec(key);
  if (!m) return null;
  const ext = m[1].toLowerCase();
  return MIME_TYPES[ext] || null;
}

/** @type {Record<string,string>} */
const MIME_TYPES = {
  txt: "text/plain",
  text: "text/plain",
  md: "text/markdown",
  markdown: "text/markdown",
  csv: "text/csv",
  tsv: "text/tab-separated-values",
  html: "text/html",
  htm: "text/html",
  css: "text/css",
  js: "text/javascript",
  mjs: "text/javascript",
  cjs: "text/javascript",
  ts: "text/x-typescript",
  json: "application/json",
  jsonl: "application/x-ndjson",
  ndjson: "application/x-ndjson",
  xml: "application/xml",
  yaml: "application/yaml",
  yml: "application/yaml",
  toml: "application/toml",
  pdf: "application/pdf",
  doc: "application/msword",
  docx: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
  xls: "application/vnd.ms-excel",
  xlsx: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
  ppt: "application/vnd.ms-powerpoint",
  pptx: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
  zip: "application/zip",
  gz: "application/gzip",
  tar: "application/x-tar",
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  svg: "image/svg+xml",
  bmp: "image/bmp",
  tif: "image/tiff",
  tiff: "image/tiff",
  ico: "image/x-icon",
  mp3: "audio/mpeg",
  wav: "audio/wav",
  ogg: "audio/ogg",
  mp4: "video/mp4",
  webm: "video/webm",
  mov: "video/quicktime",
  bin: "application/octet-stream",
};

export default Client;
