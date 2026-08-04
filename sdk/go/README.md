# aero-vault Go SDK

A native Go client for the [aero-vault](https://github.com/aero-vault/aero-vault) AI-native file platform — object storage with built-in semantic search, RAG chat, and a tool-calling agent.

**Standard library only — zero third-party dependencies.**

## Install

```sh
go get github.com/aero-vault/aero-vault-go/aerovault
```

```go
import aerovault "github.com/aero-vault/aero-vault-go/aerovault"
```

Requires Go 1.26+.

## Quickstart

```go
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	aerovault "github.com/aero-vault/aero-vault-go/aerovault"
)

func main() {
	c, err := aerovault.New("http://localhost:8080",
		aerovault.WithToken("prod-rw"),
		aerovault.WithTenant("acme"),
	)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	// Upload
	obj, err := c.Upload(ctx, "docs/readme.txt",
		strings.NewReader("hello world"),
		aerovault.UploadOptions{ContentType: "text/plain"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("uploaded", obj.Key, obj.Size, "bytes")

	// Download
	rc, _, err := c.Get(ctx, "docs/readme.txt")
	if err != nil {
		log.Fatal(err)
	}
	defer rc.Close()
	io.Copy(os.Stdout, rc)

	// Semantic search
	res, _ := c.Search(ctx, aerovault.SearchRequest{Query: "hello", K: 5})
	for _, h := range res.Hits {
		fmt.Printf("%.4f  %s\n", h.Score, h.ObjectKey)
	}

	// RAG chat (streaming)
	c.ChatStream(ctx, aerovault.ChatRequest{Query: "what does the readme say?"},
		func(tok string) { fmt.Print(tok) })
}
```

## Configuration

`New(baseURL string, opts ...Option)` accepts these options:

| Option | Description |
| --- | --- |
| `WithToken(token)` | API key or JWT, sent as `Authorization: Bearer <token>`. |
| `WithAPIKeyHeader()` | Send the token in the `X-Api-Key` header instead. |
| `WithTenant(tenant)` | Value for the `X-Aero-Tenant` header (default `"default"`). |
| `WithHTTPClient(hc)` | Supply a custom `*http.Client` (timeouts, transport, proxy). |
| `WithUserAgent(ua)` | Override the default `User-Agent`. |

Set a request timeout by supplying your own client, or use a `context.WithTimeout`:

```go
c, _ := aerovault.New(url,
	aerovault.WithToken("t"),
	aerovault.WithHTTPClient(&http.Client{Timeout: 30 * time.Second}))
```

Every method takes a `context.Context` as its first argument for cancellation and deadlines.

## API

### Files

| Method | Endpoint |
| --- | --- |
| `Upload(ctx, key, r, UploadOptions)` | `PUT /v1/files/<key>` |
| `Get(ctx, key) (io.ReadCloser, *Object, error)` | `GET /v1/files/<key>` |
| `GetVersion(ctx, key, version)` | `GET /v1/files/<key>?version=ID` |
| `GetRange(ctx, key, offset, length)` | `GET` with `Range:` → 206 |
| `Download(ctx, key, dst) (int64, error)` | `GET`, streamed to an `io.Writer` |
| `Stat(ctx, key) (*Object, error)` | `HEAD /v1/files/<key>` |
| `Exists(ctx, key) (bool, error)` | `HEAD`, mapping 404 → false |
| `List(ctx, ListOptions) (*ListPage, error)` | `GET /v1/files` |
| `IterObjects(ctx, prefix, pageSize, func(Object) error)` | auto-paginated `GET /v1/files` |
| `Delete(ctx, key, hard bool)` | `DELETE /v1/files/<key>?hard=1` |
| `GetTags` / `PutTags` / `DeleteTags` | `…/<key>/tags` |
| `ListVersions(ctx, key) ([]ObjectVersion, error)` | `GET …/<key>/versions` |
| `GetACL` / `SetACL` | `…/<key>/acl` |
| `Thumbnail(ctx, key, w, h) ([]byte, error)` | `GET …/<key>/thumbnail?w=&h=` (JPEG) |
| `Presign(ctx, key, op, expires) (*Presigned, error)` | `POST …/<key>/presign?op=get\|put` |

`Get`, `GetVersion`, and `GetRange` return an `io.ReadCloser` the caller **must close**.

### AI

| Method | Endpoint |
| --- | --- |
| `Search(ctx, SearchRequest) (*SearchResponse, error)` | `POST /v1/search` |
| `Chat(ctx, ChatRequest) (*ChatResponse, error)` | `POST /v1/chat` |
| `ChatStream(ctx, ChatRequest, func(token string)) (*ChatResponse, error)` | `POST /v1/chat/stream` (SSE) |
| `Agent(ctx, query) (*AgentResponse, error)` | `POST /v1/agent` |

`ChatStream` invokes the callback for each token as it streams in, and returns
the final `*ChatResponse` (answer + citations) parsed from the terminal `done`
event. `SearchRequest.Mode` is one of `vector`, `bm25`, or `hybrid`.

### Ops

| Method | Endpoint |
| --- | --- |
| `Usage(ctx) (*Usage, error)` | `GET /v1/usage` |
| `Health(ctx) (bool, error)` | `GET /healthz` |

### Enterprise files and distribution

```go
published, _ := c.PublishAsset(ctx, aerovault.PublishAssetRequest{
	Key: "blog/hero.jpg", Slug: "blog/hero.jpg",
	CacheControl: "public, max-age=86400",
})
fmt.Println(published.URL)

share, _ := c.CreateShare(ctx, aerovault.ShareRequest{
	Key: "review/design.png", AllowPreview: true,
	AllowDownload: true, TTLSeconds: 3600,
})
fmt.Println(share.URL) // raw token appears only here

backup, _ := c.ExportArchive(ctx, "default", "blog/")
defer backup.Close()
io.Copy(backupFile, backup)
```

`PutACL`, `CreateDepartment`, and `PutDepartmentMember` expose the enterprise
authorization surface. The same decisions apply to REST, S3, WebDAV, MCP, and
AI retrieval because enforcement is inside FileService.

Management code can complete each lifecycle with `ListShares`/`RevokeShare`,
`ListAssets`/`UnpublishAsset`, `ListResourceACL`/`DeleteResourceACL`, and
`ListDepartments`/`GetDepartment`/`DeleteDepartment`/
`DeleteDepartmentMember`.

## Error handling

Any non-2xx response is returned as `*aerovault.Error`, which carries the HTTP
status plus the platform's `{"error":{"code","message","request_id"}}` envelope:

```go
_, err := c.Stat(ctx, "missing")
var apiErr *aerovault.Error
if aerovault.AsError(err, &apiErr) { // or errors.As(err, &apiErr)
	switch apiErr.Status {
	case 404:
		fmt.Println("not found:", apiErr.Code)
	case 507:
		fmt.Println("quota exceeded; request id:", apiErr.RequestID)
	}
}
```

`*aerovault.Error` implements `error` and works with `errors.As` / `errors.Is`.

## Metadata, tags, and content type

- **Metadata** is set at upload time via `UploadOptions.Metadata` (sent as
  `X-Meta-<key>` headers) and read back on the returned `*Object`.
- **Tags** are mutable post-upload via `PutTags` / `GetTags` / `DeleteTags`.
- **Content type** is sent on every upload. If you omit it, the SDK infers one
  from the key's extension, falling back to `application/octet-stream` — always
  sending a type keeps the object eligible for the server's text indexing.

## License

MIT
