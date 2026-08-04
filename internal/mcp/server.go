package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/aero-vault/aero-vault/internal/ai"
	mw "github.com/aero-vault/aero-vault/internal/middleware"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
)

// Server is a transport-agnostic MCP server. It binds tools/resources to an
// existing FileService + Search.
type Server struct {
	svc    *service.FileService
	repo   repository.Repository
	search *ai.Search
	chat   *ai.Chat
	tenant string // active tenant for resources (default "default")
	logger *slog.Logger
}

func NewServer(svc *service.FileService, repo repository.Repository, search *ai.Search, tenant string, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	if tenant == "" {
		tenant = "default"
	}
	return &Server{svc: svc, repo: repo, search: search, tenant: tenant, logger: logger}
}

// WithChat wires a Chat service into the server, enabling the chat tool.
func (s *Server) WithChat(c *ai.Chat) *Server {
	s.chat = c
	return s
}

// tenantFor returns the request-scoped tenant from ctx if present (set by the
// HTTP middleware), falling back to the server's default tenant (stdio mode).
func (s *Server) tenantFor(ctx context.Context) string {
	if t, ok := mw.TenantFromContext(ctx); ok {
		return t
	}
	return s.tenant
}

// Handle reads one JSON-RPC request and produces one response. Notifications
// (ID is null) return nil. Used by both the stdio and HTTP transports.
func (s *Server) Handle(ctx context.Context, raw []byte) []byte {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return marshalErr(nil, -32700, "parse error")
	}
	if req.JSONRPC != "" && req.JSONRPC != "2.0" {
		return marshalErr(req.ID, -32600, "invalid jsonrpc")
	}
	if len(req.ID) == 0 {
		// notification: dispatch but do not respond.
		_, _ = s.dispatch(ctx, req)
		return nil
	}
	result, rerr := s.dispatch(ctx, req)
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if rerr != nil {
		resp.Error = rerr
	} else {
		resp.Result = result
	}
	out, _ := json.Marshal(resp)
	return out
}

func (s *Server) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return initializeResult{
			ProtocolVersion: ProtocolVersion,
			ServerInfo:      map[string]any{"name": "aero-vault", "version": "0.2.0"},
			Capabilities: map[string]any{
				"tools":     map[string]any{"listChanged": false},
				"resources": map[string]any{"listChanged": false, "subscribe": false},
			},
		}, nil

	case "tools/list":
		return s.listTools(), nil

	case "tools/call":
		return s.callTool(ctx, req.Params)

	case "resources/list":
		return s.listResources(ctx)

	case "resources/read":
		return s.readResource(ctx, req.Params)

	case "ping":
		return map[string]any{}, nil

	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func (s *Server) listTools() listToolsResult {
	tools := []tool{
		{
			Name:        "list_files",
			Description: "List object keys in a bucket. Default bucket: 'default'.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"bucket": map[string]any{"type": "string"},
					"prefix": map[string]any{"type": "string"},
					"limit":  map[string]any{"type": "integer"},
				},
			},
		},
		{
			Name:        "read_file",
			Description: "Read the full text of an object by bucket+key. Records an audit row.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"bucket": map[string]any{"type": "string"},
					"key":    map[string]any{"type": "string"},
				},
				"required": []string{"key"},
			},
		},
		{
			Name:        "write_file",
			Description: "Write text content to an object key.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"key":          map[string]any{"type": "string", "description": "Object key to write"},
					"content":      map[string]any{"type": "string", "description": "Text content to store"},
					"content_type": map[string]any{"type": "string", "description": "MIME type (default: text/plain)"},
				},
				"required": []string{"key", "content"},
			},
		},
		{
			Name:        "delete_file",
			Description: "Soft-delete an object by key.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"key": map[string]any{"type": "string", "description": "Object key to delete"},
				},
				"required": []string{"key"},
			},
		},
	}
	if s.search != nil {
		tools = append(tools, tool{
			Name:        "search",
			Description: "Semantic search over indexed chunks. Returns ranked text snippets with source object references.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":  map[string]any{"type": "string"},
					"bucket": map[string]any{"type": "string"},
					"k":      map[string]any{"type": "integer"},
				},
				"required": []string{"query"},
			},
		})
	}
	if s.chat != nil {
		tools = append(tools, tool{
			Name:        "chat",
			Description: "Answer a question using RAG over the knowledge vault.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Question to answer using the knowledge vault"},
					"k":     map[string]any{"type": "integer", "description": "Number of chunks to retrieve (default 5)"},
				},
				"required": []string{"query"},
			},
		})
	}
	return listToolsResult{Tools: tools}
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *Server) callTool(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var p toolCallParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params"}
	}
	switch p.Name {
	case "list_files":
		return s.toolListFiles(ctx, p.Arguments)
	case "read_file":
		return s.toolReadFile(ctx, p.Arguments)
	case "write_file":
		return s.toolWriteFile(ctx, p.Arguments)
	case "delete_file":
		return s.toolDeleteFile(ctx, p.Arguments)
	case "search":
		return s.toolSearch(ctx, p.Arguments)
	case "chat":
		return s.toolChat(ctx, p.Arguments)
	default:
		return nil, &rpcError{Code: -32601, Message: "unknown tool: " + p.Name}
	}
}

func (s *Server) toolListFiles(ctx context.Context, args map[string]any) (any, *rpcError) {
	bucket := stringArg(args, "bucket", service.DefaultBucket)
	prefix := stringArg(args, "prefix", "")
	limit := intArg(args, "limit", 50)
	page, err := s.svc.List(ctx, s.tenantFor(ctx), bucket, prefix, "", limit)
	if err != nil {
		return errResult(err), nil
	}
	var b strings.Builder
	for _, o := range page.Objects {
		fmt.Fprintf(&b, "%s/%s\t%d bytes\t%s\n", o.Bucket, o.Key, o.Size, o.ContentType)
	}
	return toolResult{Content: []contentBlock{{Type: "text", Text: b.String()}}}, nil
}

func (s *Server) toolReadFile(ctx context.Context, args map[string]any) (any, *rpcError) {
	bucket := stringArg(args, "bucket", service.DefaultBucket)
	key := stringArg(args, "key", "")
	if key == "" {
		return errResult(errors.New("key required")), nil
	}
	rc, obj, err := s.svc.Get(ctx, s.tenantFor(ctx), bucket, key)
	if err != nil {
		return errResult(err), nil
	}
	defer rc.Close()
	body, err := readTextObject(rc)
	if err != nil {
		return errResult(err), nil
	}
	// Audit: MCP read = AI consumption of this file.
	_ = s.repo.RecordUsage(ctx, repository.Usage{
		TenantID:  s.tenantFor(ctx),
		Caller:    "mcp:read",
		Query:     "",
		ObjectIDs: []int64{obj.ID},
	})
	return toolResult{Content: []contentBlock{{Type: "text", Text: string(body)}}}, nil
}

func (s *Server) toolSearch(ctx context.Context, args map[string]any) (any, *rpcError) {
	if s.search == nil {
		return errResult(errors.New("search not enabled")), nil
	}
	query := stringArg(args, "query", "")
	if query == "" {
		return errResult(errors.New("query required")), nil
	}
	bucket := stringArg(args, "bucket", "")
	k := intArg(args, "k", 10)
	hits, err := s.search.Query(ctx, ai.Request{
		Tenant: s.tenantFor(ctx), Bucket: bucket, Query: query, K: k, Caller: "mcp:search",
	})
	if err != nil {
		return errResult(err), nil
	}
	var b strings.Builder
	for i, h := range hits {
		fmt.Fprintf(&b, "[%d] score=%.4f  %s/%s#chunk-%d\n%s\n\n", i+1, h.Score, h.Bucket, h.ObjectKey, h.Seq, h.Chunk)
	}
	if b.Len() == 0 {
		b.WriteString("(no matches)")
	}
	return toolResult{Content: []contentBlock{{Type: "text", Text: b.String()}}}, nil
}

func (s *Server) toolWriteFile(ctx context.Context, args map[string]any) (any, *rpcError) {
	key := stringArg(args, "key", "")
	if key == "" {
		return errResult(errors.New("key required")), nil
	}
	content, ok := args["content"].(string)
	if !ok {
		return errResult(errors.New("content required")), nil
	}
	ct := stringArg(args, "content_type", "text/plain")
	_, err := s.svc.Put(ctx, s.tenantFor(ctx), service.DefaultBucket, key, strings.NewReader(content), int64(len(content)), service.PutOptions{ContentType: ct})
	if err != nil {
		return errResult(err), nil
	}
	return toolResult{Content: []contentBlock{{Type: "text", Text: fmt.Sprintf("written: %s (%d bytes)", key, len(content))}}}, nil
}

func (s *Server) toolDeleteFile(ctx context.Context, args map[string]any) (any, *rpcError) {
	key := stringArg(args, "key", "")
	if key == "" {
		return errResult(errors.New("key required")), nil
	}
	if err := s.svc.Delete(ctx, s.tenantFor(ctx), service.DefaultBucket, key, false); err != nil {
		return errResult(err), nil
	}
	return toolResult{Content: []contentBlock{{Type: "text", Text: "deleted: " + key}}}, nil
}

func (s *Server) toolChat(ctx context.Context, args map[string]any) (any, *rpcError) {
	if s.chat == nil {
		return errResult(errors.New("chat not enabled")), nil
	}
	query := stringArg(args, "query", "")
	if query == "" {
		return errResult(errors.New("query required")), nil
	}
	k := intArg(args, "k", 5)
	resp, err := s.chat.Answer(ctx, ai.ChatReq{Tenant: s.tenantFor(ctx), Query: query, K: k, Caller: "mcp:chat"})
	if err != nil {
		return errResult(err), nil
	}
	return toolResult{Content: []contentBlock{{Type: "text", Text: resp.Answer}}}, nil
}

func (s *Server) listResources(ctx context.Context) (any, *rpcError) {
	page, err := s.svc.List(ctx, s.tenantFor(ctx), service.DefaultBucket, "", "", 200)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: err.Error()}
	}
	out := listResourcesResult{Resources: make([]resource, 0, len(page.Objects))}
	for _, o := range page.Objects {
		uri := fmt.Sprintf("aero-vault://%s/%s/%s", o.TenantID, o.Bucket, o.Key)
		out.Resources = append(out.Resources, resource{
			URI: uri, Name: o.Key, MimeType: o.ContentType,
		})
	}
	return out, nil
}

type readResourceParams struct {
	URI string `json:"uri"`
}

func (s *Server) readResource(ctx context.Context, raw json.RawMessage) (any, *rpcError) {
	var p readResourceParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid params"}
	}
	// uri form: aero-vault://{tenant}/{bucket}/{key...}
	prefix := "aero-vault://"
	if !strings.HasPrefix(p.URI, prefix) {
		return nil, &rpcError{Code: -32602, Message: "uri must start with aero-vault://"}
	}
	rest := strings.TrimPrefix(p.URI, prefix)
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 3 {
		return nil, &rpcError{Code: -32602, Message: "uri must be aero-vault://{tenant}/{bucket}/{key}"}
	}
	// Security: enforce tenant boundary. The URI tenant must match the
	// request-scoped tenant from the middleware or the server default (stdio).
	// This prevents cross-tenant data access via crafted URIs.
	uriTenant := parts[0]
	allowedTenant := s.tenantFor(ctx)
	if uriTenant != allowedTenant {
		return nil, &rpcError{Code: -32000, Message: fmt.Sprintf("tenant mismatch: requested %q but authenticated as %q", uriTenant, allowedTenant)}
	}
	rc, obj, err := s.svc.Get(ctx, allowedTenant, parts[1], parts[2])
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: err.Error()}
	}
	defer rc.Close()
	body, err := readTextObject(rc)
	if err != nil {
		return nil, &rpcError{Code: -32000, Message: "read object: " + err.Error()}
	}
	return readResourceResult{Contents: []resourceContent{{
		URI: p.URI, MimeType: obj.ContentType, Text: string(body),
	}}}, nil
}

func readTextObject(reader io.Reader) ([]byte, error) {
	const maxTextBytes = 4 << 20
	body, err := io.ReadAll(io.LimitReader(reader, maxTextBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxTextBytes {
		return nil, errors.New("object exceeds 4 MiB MCP text limit")
	}
	return body, nil
}

func errResult(err error) toolResult {
	return toolResult{
		Content: []contentBlock{{Type: "text", Text: err.Error()}},
		IsError: true,
	}
}

func marshalErr(id json.RawMessage, code int, msg string) []byte {
	if id == nil {
		id = json.RawMessage("null")
	}
	b, _ := json.Marshal(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
	return b
}

func stringArg(m map[string]any, key, def string) string {
	v, ok := m[key]
	if !ok {
		return def
	}
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

func intArg(m map[string]any, key string, def int) int {
	v, ok := m[key]
	if !ok {
		return def
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return def
}
