// Type declarations for @aero-vault/sdk
// Hand-written to mirror the runtime shape of aero-vault.js.

/** SDK version string. */
export declare const VERSION: string;

/** A request body may be raw bytes, text, or a streamable Web body. */
export type Body =
  | string
  | Uint8Array
  | ArrayBuffer
  | ArrayBufferView
  | Blob
  | ReadableStream;

/** Canned ACLs understood by the platform. */
export type CannedAcl =
  | "private"
  | "public-read"
  | "public-read-write"
  | "authenticated-read";

/** Retrieval mode for search/chat. */
export type SearchMode = "vector" | "bm25" | "hybrid";

/** Stored object metadata, as returned by upload/list/stat. */
export interface AeroObject {
  bucket: string;
  key: string;
  size: number;
  etag: string;
  content_type: string;
  backend: string;
  metadata: Record<string, string>;
  tags: Record<string, string>;
  created_at: string;
  updated_at: string;
}

/** One ranked result from /v1/search (also used as a chat citation). */
export interface SearchHit {
  score: number;
  chunk: string;
  chunk_id: number;
  object_id: number;
  bucket: string;
  object_key: string;
  seq: number;
  embed_model: string;
}

/** Answer plus grounding citations from /v1/chat. */
export interface ChatResponse {
  answer: string;
  model: string;
  citations: SearchHit[];
}

/** A single page of a list response from GET /v1/files. */
export interface ListPage {
  objects: AeroObject[];
  next_marker?: string;
  has_more?: boolean;
}

/** Result of a multipart upload init (POST /v1/multipart). */
export interface MultipartUpload {
  upload_id: string;
  key: string;
  bucket: string;
}

/** Current tenant usage (GET /v1/usage). */
export interface Usage {
  tenant?: string;
  used_bytes?: number;
  used_objects?: number;
  max_bytes?: number;
  max_objects?: number;
}

/** Result of the tool-calling agent loop (POST /v1/agent). */
export interface AgentResponse {
  answer?: string;
  steps?: unknown;
  model?: string;
}

/** One AI-consumption record for an object (GET /v1/lineage/objects/{id}). */
export interface LineageEntry {
  usage_id: number;
  caller: string;
  query?: string;
  chunk_ids: number[];
  object_ids: number[];
  request_id?: string;
  created_at: string;
  model?: string;
  prompt_tokens?: number;
  completion_tokens?: number;
  total_tokens?: number;
  latency_ms?: number;
  cost_micros?: number;
}

/** AI consumption history for an object (GET /v1/lineage/objects/{id}). */
export interface LineageResponse {
  object_id: number;
  entries: LineageEntry[];
}

/** A minimal subset of the Fetch API's `fetch` signature. */
export type FetchLike = (
  input: string | URL | Request,
  init?: RequestInit
) => Promise<Response>;

/** Options accepted by the {@link Client} constructor. */
export interface ClientOptions {
  /** API key or JWT. Falls back to `AERO_VAULT_TOKEN`. */
  token?: string;
  /** Value for `X-Aero-Tenant`. Falls back to `AERO_VAULT_TENANT`, then "default". */
  tenant?: string;
  /** Send the token as `X-Api-Key` instead of `Authorization: Bearer`. */
  apiKeyHeader?: boolean;
  /** Custom fetch implementation (defaults to global `fetch`). */
  fetch?: FetchLike;
  /** Per-request timeout in milliseconds (default 30000; 0 disables). */
  timeout?: number;
}

/** Options for {@link Client.upload}. */
export interface UploadOptions {
  contentType?: string;
  metadata?: Record<string, string>;
}

/** Options for {@link Client.get} / {@link Client.getText}. */
export interface GetOptions {
  version?: string;
}

/** Options for {@link Client.getResponse}. */
export interface GetResponseOptions {
  version?: string;
  /** A `Range` header value, e.g. "bytes=0-1023". */
  range?: string;
  /** An ETag for a conditional `If-None-Match` request (304 is not an error). */
  ifNoneMatch?: string;
}

/** Options for {@link Client.list}. */
export interface ListOptions {
  prefix?: string;
  marker?: string;
  limit?: number;
}

/** Options for {@link Client.iterObjects}. */
export interface IterOptions {
  prefix?: string;
  pageSize?: number;
}

/** Options for {@link Client.delete}. */
export interface DeleteOptions {
  hard?: boolean;
}

/** Options for {@link Client.thumbnail}. */
export interface ThumbnailOptions {
  w?: number;
  h?: number;
}

/** Options for {@link Client.search}. */
export interface SearchOptions {
  k?: number;
  mode?: SearchMode;
  bucket?: string;
}

/** Options for {@link Client.chat}. */
export interface ChatOptions {
  k?: number;
  mode?: SearchMode;
  bucket?: string;
  temperature?: number;
  prior?: Array<Record<string, string>>;
}

/** Options for {@link Client.chatStream}. */
export interface ChatStreamOptions {
  k?: number;
  mode?: SearchMode;
  bucket?: string;
  /** Invoked with the final response once the stream completes. */
  onDone?: (resp: ChatResponse) => void;
  /** Abort the stream early. */
  signal?: AbortSignal;
}

/** Options for {@link Client.createMultipartUpload}. */
export interface CreateMultipartOptions {
  contentType?: string;
}

export interface ShareOptions {
  bucket?: string;
  name?: string;
  password?: string;
  allowPreview?: boolean;
  allowDownload?: boolean;
  maxUses?: number;
  ttlSeconds?: number;
}

export interface PublishAssetOptions {
  bucket?: string;
  cacheControl?: string;
}

export interface ResourceACLInput {
  bucket?: string;
  key?: string;
  resource_kind: "bucket" | "folder" | "object";
  principal_type: "user" | "department" | "group" | "role" | "authenticated" | "everyone";
  principal_id?: string;
  actions: string[];
  effect: "allow" | "deny";
  inherit?: boolean;
}

export interface ResourceACLQuery {
  bucket?: string;
  resourceKind?: "object" | "folder" | "bucket";
}

/**
 * Raised when the server returns a non-2xx response.
 * Carries the platform error envelope fields when present.
 */
export declare class AeroVaultError extends Error {
  constructor(status: number, code?: string, message?: string, requestId?: string);
  name: "AeroVaultError";
  /** HTTP status code (0 for network/timeout errors). */
  status: number;
  /** Machine-readable error code. */
  code: string;
  /** Bare server message (the envelope's `message` field). */
  message: string;
  /** Server request id, for support correlation. */
  requestId: string;
  /** `[status code] message (request_id=...)`. */
  toString(): string;
}

/** HTTP client for an aero-vault server. */
export declare class Client {
  constructor(baseUrl?: string, options?: ClientOptions);

  readonly baseUrl: string;
  token: string | null;
  tenant: string;
  apiKeyHeader: boolean;
  timeout: number;

  // ---- files ----
  upload(key: string, data: Body, opts?: UploadOptions): Promise<AeroObject>;
  get(key: string, opts?: GetOptions): Promise<Uint8Array>;
  getText(key: string, opts?: GetOptions): Promise<string>;
  getResponse(key: string, opts?: GetResponseOptions): Promise<Response>;
  stat(key: string): Promise<AeroObject>;
  exists(key: string): Promise<boolean>;
  list(opts?: ListOptions): Promise<ListPage>;
  iterObjects(opts?: IterOptions): AsyncGenerator<AeroObject, void, unknown>;
  delete(key: string, opts?: DeleteOptions): Promise<void>;
  presign(key: string, op?: "get" | "put", expires?: number): Promise<Record<string, unknown>>;
  thumbnail(key: string, opts?: ThumbnailOptions): Promise<Uint8Array>;

  // ---- tags / versions / acl ----
  getTags(key: string): Promise<Record<string, string>>;
  putTags(key: string, tags: Record<string, string>): Promise<unknown>;
  deleteTags(key: string): Promise<void>;
  listVersions(key: string): Promise<unknown>;
  /** Lock an object for N seconds. */
  lock(key: string, seconds: number): Promise<unknown>;
  getAcl(key: string): Promise<{ acl: string }>;
  setAcl(key: string, acl: CannedAcl): Promise<unknown>;
  getBucketACL(bucket: string): Promise<{ acl: string }>;
  setBucketACL(bucket: string, acl: CannedAcl): Promise<unknown>;

  // ---- multipart ----
  createMultipartUpload(key: string, opts?: CreateMultipartOptions): Promise<MultipartUpload>;
  uploadPart(uploadId: string, partNumber: number, data: Body): Promise<unknown>;
  completeMultipartUpload(uploadId: string): Promise<AeroObject>;
  abortMultipartUpload(uploadId: string): Promise<void>;

  // ---- AI ----
  search(query: string, opts?: SearchOptions): Promise<SearchHit[]>;
  chat(query: string, opts?: ChatOptions): Promise<ChatResponse>;
  chatStream(query: string, opts?: ChatStreamOptions): AsyncGenerator<string, void, unknown>;
  agent(query: string): Promise<AgentResponse>;
  lineage(objectId: number, limit?: number): Promise<LineageResponse>;

  // ---- ops ----
  usage(): Promise<Usage>;
  health(): Promise<boolean>;

  // ---- enterprise access / sharing / publishing ----
  createShare(key: string, opts?: ShareOptions): Promise<Record<string, any>>;
  listShares(key: string, bucket?: string): Promise<any[]>;
  revokeShare(id: string): Promise<void>;
  publishAsset(key: string, slug: string, opts?: PublishAssetOptions): Promise<Record<string, any>>;
  unpublishAsset(slug: string): Promise<void>;
  listAssets(): Promise<any[]>;
  putResourceACL(input: ResourceACLInput): Promise<Record<string, any>>;
  listResourceACL(key?: string, opts?: ResourceACLQuery): Promise<any[]>;
  deleteResourceACL(id: string): Promise<void>;
  createDepartment(name: string, parentId?: string): Promise<Record<string, any>>;
  listDepartments(): Promise<any[]>;
  getDepartment(id: string): Promise<Record<string, any>>;
  deleteDepartment(id: string): Promise<void>;
  putDepartmentMember(departmentId: string, subjectId: string, role?: "member" | "manager"): Promise<void>;
  deleteDepartmentMember(departmentId: string, subjectId: string): Promise<void>;
  exportArchive(opts?: { bucket?: string; prefix?: string }): Promise<Uint8Array>;
}

/** Percent-encode a key's path segments while preserving `/` separators. */
export declare function escapeKey(key: string): string;

/** Coerce an SDK {@link Body} into a fetch-compatible BodyInit. */
export declare function coerceBody(data: Body): BodyInit;

/** Guess a Content-Type from a key's file extension; null when unknown. */
export declare function guessContentType(key: string): string | null;

/** Parse an SSE byte stream into `[event, data]` pairs. */
export declare function iterSSE(
  stream: ReadableStream<Uint8Array>
): AsyncGenerator<[string, string], void, unknown>;

export default Client;
