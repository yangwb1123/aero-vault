package aerovault

import (
	"fmt"
	"time"
)

// Object is a stored object's metadata, as returned by Upload, List, Stat and
// the multipart-complete endpoints. It mirrors the server's `objectDTO`.
type Object struct {
	Bucket      string            `json:"bucket"`
	Key         string            `json:"key"`
	Size        int64             `json:"size"`
	ETag        string            `json:"etag"`
	ContentType string            `json:"content_type,omitempty"`
	Backend     string            `json:"backend"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// ListPage is a single page of a List call.
type ListPage struct {
	Objects    []Object `json:"objects"`
	NextMarker string   `json:"next_marker,omitempty"`
	HasMore    bool     `json:"has_more"`
}

// SearchHit is one ranked result from Search. It is also reused as a chat
// citation in ChatResponse.Citations.
type SearchHit struct {
	Score      float64 `json:"score"`
	Chunk      string  `json:"chunk"`
	ChunkID    int64   `json:"chunk_id"`
	ObjectID   int64   `json:"object_id"`
	Bucket     string  `json:"bucket"`
	ObjectKey  string  `json:"object_key"`
	Seq        int     `json:"seq"`
	EmbedModel string  `json:"embed_model"`
}

// SearchRequest is the body of a Search call. Mode is one of
// "vector", "bm25" or "hybrid"; empty lets the server choose its default.
type SearchRequest struct {
	Query  string `json:"query"`
	K      int    `json:"k,omitempty"`
	Mode   string `json:"mode,omitempty"`
	Bucket string `json:"bucket,omitempty"`
}

// SearchResponse is the envelope returned by POST /v1/search.
type SearchResponse struct {
	Query string      `json:"query"`
	Hits  []SearchHit `json:"hits"`
}

// ChatMessage is a single prior turn supplied to Chat for multi-turn context.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is the body of a Chat or ChatStream call. Only Query is
// required; zero-valued optional fields are omitted from the wire body.
// Temperature and Prior are honored by Chat but ignored by ChatStream
// (matching the server, which decodes a narrower body for the stream route).
type ChatRequest struct {
	Query       string        `json:"query"`
	K           int           `json:"k,omitempty"`
	Mode        string        `json:"mode,omitempty"`
	Bucket      string        `json:"bucket,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	Prior       []ChatMessage `json:"prior,omitempty"`
}

// ChatResponse is the answer plus grounding citations from POST /v1/chat,
// and is also the payload of the SSE "done" frame from /v1/chat/stream.
type ChatResponse struct {
	Answer    string      `json:"answer"`
	Model     string      `json:"model"`
	Citations []SearchHit `json:"citations"`
}

// AgentStep is one tool-call cycle recorded by the agent loop. Its concrete
// fields are server-defined; it is decoded loosely so the SDK stays forward
// compatible.
type AgentStep map[string]any

// AgentResponse is the result of POST /v1/agent.
type AgentResponse struct {
	Answer string      `json:"answer"`
	Steps  []AgentStep `json:"steps"`
	Model  string      `json:"model"`
}

// ObjectVersion is one historical revision of an object, from ListVersions.
type ObjectVersion struct {
	VersionID   string     `json:"version_id"`
	Size        int64      `json:"size"`
	ETag        string     `json:"etag"`
	ContentType string     `json:"content_type,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at,omitempty"`
	LockedUntil *time.Time `json:"locked_until,omitempty"`
}

// Presigned is the result of a Presign call: a temporary URL and its expiry.
type Presigned struct {
	URL     string    `json:"url"`
	Expires time.Time `json:"expires"`
}

// Usage reports a tenant's current consumption and quota (GET /v1/usage).
type Usage struct {
	Tenant      string `json:"tenant"`
	UsedBytes   int64  `json:"used_bytes"`
	UsedObjects int64  `json:"used_objects"`
	MaxBytes    int64  `json:"max_bytes"`
	MaxObjects  int64  `json:"max_objects"`
}

// Error is returned for any non-2xx HTTP response. It carries the HTTP status
// plus the platform's error envelope ({"error":{"code","message","request_id"}})
// when present; for non-JSON bodies the raw text lands in Message.
type Error struct {
	Status    int    // HTTP status code
	Code      string // machine-readable code, e.g. "NotFound"
	Message   string // human-readable detail
	RequestID string // server request id, if any
}

func (e *Error) Error() string {
	code := e.Code
	if code == "" {
		code = "HTTPError"
	}
	msg := e.Message
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", e.Status)
	}
	if e.RequestID != "" {
		return fmt.Sprintf("aerovault: [%d %s] %s (request_id=%s)", e.Status, code, msg, e.RequestID)
	}
	return fmt.Sprintf("aerovault: [%d %s] %s", e.Status, code, msg)
}
