package rest

import (
	"encoding/json"
	"sync"
)

// apiRoute describes one REST endpoint for spec generation.
type apiRoute struct {
	Method      string
	Path        string
	Summary     string
	Tag         string
	Body        string // example JSON body for request, or "" for no body
	Status      int    // success HTTP status
	Response    string // example JSON response, or "" for empty
	Response200 string // example 200 response when Status != 200
	AdminOnly   bool
}

// specBuilder constructs the full OpenAPI 3.1.0 document from a route table.
// Call Refresh() after updating the route table, then JSON() to serialize.
type specBuilder struct {
	mu     sync.RWMutex
	routes []apiRoute
	cached []byte
}

var globalSpec = &specBuilder{}

// RegisterRoute adds an endpoint to the global spec.
func RegisterRoute(r apiRoute) {
	globalSpec.mu.Lock()
	defer globalSpec.mu.Unlock()
	globalSpec.routes = append(globalSpec.routes, r)
	globalSpec.cached = nil // invalidate
}

// RegisterRoutes adds multiple endpoints at once.
func RegisterRoutes(rs []apiRoute) {
	globalSpec.mu.Lock()
	defer globalSpec.mu.Unlock()
	globalSpec.routes = append(globalSpec.routes, rs...)
	globalSpec.cached = nil
}
// JSON returns the cached OpenAPI spec as JSON, rebuilding if routes changed.
func (s *specBuilder) JSON() []byte {
	s.mu.RLock()
	cached := s.cached
	s.mu.RUnlock()
	if cached != nil {
		return cached
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := s.build()
	b, _ := json.MarshalIndent(doc, "", "  ")
	s.cached = b
	return b
}

func (s *specBuilder) build() map[string]any {
	paths := map[string]any{}
	sch := s.schemas()

	for _, r := range s.routes {
		p := r.Path
		if _, ok := paths[p]; !ok {
			paths[p] = map[string]any{}
		}
		methods := paths[p].(map[string]any)

		op := map[string]any{
			"summary":     r.Summary,
			"tags":        []string{r.Tag},
			"security":    []map[string]any{{"bearer": []string{}, "apiKey": []string{}}},
			"parameters":  s.parameters(r),
			"responses":   s.responses(r, sch),
		}
		if r.Body != "" {
			op["requestBody"] = map[string]any{
				"required": true,
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": map[string]any{
							"type": "object",
						},
						"example": json.RawMessage(r.Body),
					},
				},
			}
		}

		methods[httpMethodLabel(r.Method)] = op
	}

	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "aero-vault",
			"version":     "0.9.0",
			"description": "AI-native file platform — REST + S3 + WebDAV + MCP protocols sharing a unified backend.",
		},
		"servers": []map[string]any{{"url": "/", "description": "Self"}},
		"tags": []map[string]any{
			{"name": "files", "description": "Object CRUD"},
			{"name": "search", "description": "Semantic + lexical retrieval"},
			{"name": "chat", "description": "RAG inference"},
			{"name": "agent", "description": "Tool-calling autonomous loop"},
			{"name": "events", "description": "Lifecycle event stream"},
			{"name": "buckets", "description": "Per-bucket policies"},
			{"name": "admin", "description": "Operator surfaces (admin scope)"},
			{"name": "legal-hold", "description": "Compliance holds"},
			{"name": "multipart", "description": "Multipart uploads"},
		},
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearer": map[string]any{"type": "http", "scheme": "bearer"},
				"apiKey": map[string]any{"type": "apiKey", "in": "header", "name": "X-Api-Key"},
			},
			"parameters": map[string]any{
				"tenant": map[string]any{
					"name": "X-Aero-Tenant", "in": "header",
					"schema": map[string]any{"type": "string", "default": "default"},
				},
				"bucket": map[string]any{
					"name": "bucket", "in": "path", "required": true,
					"schema": map[string]any{"type": "string"},
				},
				"key": map[string]any{
					"name": "key", "in": "path",
					"schema": map[string]any{"type": "string"},
				},
			},
			"schemas": sch,
		},
		"paths": paths,
	}
}

func (s *specBuilder) parameters(r apiRoute) []map[string]any {
	return []map[string]any{
		{"$ref": "#/components/parameters/tenant"},
	}
}

func (s *specBuilder) responses(r apiRoute, sch map[string]any) map[string]any {
	status := r.Status
	resps := map[string]any{}
	code := fmtStatus(status)

	resp := map[string]any{
		"description": "Success",
	}
	if r.Response != "" {
		resp["content"] = map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{"type": "object"},
				"example": json.RawMessage(r.Response),
			},
		}
	}
	resps[code] = resp

	// Add default error response.
	resps["default"] = map[string]any{
		"description": "Error response",
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"error": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"code":    map[string]any{"type": "string"},
								"message": map[string]any{"type": "string"},
							},
						},
					},
				},
			},
		},
	}
	return resps
}

func (s *specBuilder) schemas() map[string]any {
	return map[string]any{
		"Object": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"key": map[string]any{"type": "string"},
				"size": map[string]any{"type": "integer"},
				"etag": map[string]any{"type": "string"},
				"content_type": map[string]any{"type": "string"},
				"metadata": map[string]any{"type": "object"},
				"tags": map[string]any{"type": "object"},
				"storage_class": map[string]any{"type": "string"},
				"created_at": map[string]any{"type": "string", "format": "date-time"},
				"updated_at": map[string]any{"type": "string", "format": "date-time"},
			},
		},
		"BucketConfig": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string"},
				"versioning": map[string]any{"type": "boolean"},
				"object_lock_seconds": map[string]any{"type": "integer"},
				"bucket_max_bytes": map[string]any{"type": "integer"},
				"bucket_max_objects": map[string]any{"type": "integer"},
			},
		},
		"Error": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"error": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"code": map[string]any{"type": "string"},
						"message": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
}

func fmtStatus(code int) string {
	switch code {
	case 200:
		return "200"
	case 201:
		return "201"
	case 204:
		return "204"
	default:
		return "200"
	}
}

// httpMethodLabel converts "GET" -> "get" etc.
func httpMethodLabel(m string) string {
	switch m {
	case "GET":
		return "get"
	case "PUT":
		return "put"
	case "POST":
		return "post"
	case "DELETE":
		return "delete"
	case "PATCH":
		return "patch"
	case "HEAD":
		return "head"
	default:
		return m
	}
}
