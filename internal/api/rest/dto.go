package rest

import (
	"time"

	"github.com/aero-vault/aero-vault/internal/repository"
)

type errorBody struct {
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

// objectDTO is the JSON view of a stored object.
type objectDTO struct {
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

func toObjectDTO(o repository.Object) objectDTO {
	return objectDTO{
		Bucket:      o.Bucket,
		Key:         o.Key,
		Size:        o.Size,
		ETag:        o.ETag,
		ContentType: o.ContentType,
		Backend:     o.Backend,
		Metadata:    o.Metadata,
		Tags:        o.Tags,
		CreatedAt:   o.CreatedAt,
		UpdatedAt:   o.UpdatedAt,
	}
}

type listResponse struct {
	Objects    []objectDTO `json:"objects"`
	NextMarker string      `json:"next_marker,omitempty"`
	HasMore    bool        `json:"has_more"`
}

type presignResponse struct {
	URL     string    `json:"url"`
	Expires time.Time `json:"expires"`
}

type initMultipartRequest struct {
	Key         string            `json:"key"`
	ContentType string            `json:"content_type,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type initMultipartResponse struct {
	UploadID string `json:"upload_id"`
	Key      string `json:"key"`
	Bucket   string `json:"bucket"`
}

type partResponse struct {
	PartNumber int32  `json:"part_number"`
	ETag       string `json:"etag"`
	Size       int64  `json:"size"`
}

type searchRequest struct {
	Query  string `json:"query"`
	Bucket string `json:"bucket,omitempty"`
	K      int    `json:"k,omitempty"`
	Mode   string `json:"mode,omitempty"` // vector | bm25 | hybrid
}

type searchResponse struct {
	Query string      `json:"query"`
	Hits  interface{} `json:"hits"`
}

type lineageEntry struct {
	UsageID          int64     `json:"usage_id"`
	Caller           string    `json:"caller"`
	Query            string    `json:"query,omitempty"`
	ChunkIDs         []int64   `json:"chunk_ids"`
	ObjectIDs        []int64   `json:"object_ids"`
	RequestID        string    `json:"request_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	Model            string    `json:"model,omitempty"`
	PromptTokens     int       `json:"prompt_tokens,omitempty"`
	CompletionTokens int       `json:"completion_tokens,omitempty"`
	TotalTokens      int       `json:"total_tokens,omitempty"`
	LatencyMs        int64     `json:"latency_ms,omitempty"`
	CostMicros       int64     `json:"cost_micros,omitempty"`
}

type lineageResponse struct {
	ObjectID int64          `json:"object_id"`
	Entries  []lineageEntry `json:"entries"`
}
