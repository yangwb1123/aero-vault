package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/aero-vault/aero-vault/internal/service"
)

func agentToolSpecs() []ToolSpec {
	return []ToolSpec{
		{Type: "function", Function: map[string]any{
			"name":        "list_files",
			"description": "List object keys, optionally filtered by prefix.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prefix": map[string]any{"type": "string"},
					"limit":  map[string]any{"type": "integer"},
				},
			},
		}},
		{Type: "function", Function: map[string]any{
			"name":        "read_file",
			"description": "Read an object's text content (truncated to 4KB).",
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{"key": map[string]any{"type": "string"}},
				"required":   []string{"key"},
			},
		}},
		{Type: "function", Function: map[string]any{
			"name":        "search",
			"description": "Semantic search across indexed chunks.",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
					"k":     map[string]any{"type": "integer"},
				},
				"required": []string{"query"},
			},
		}},
	}
}

func (a *Agent) dispatchTool(
	ctx context.Context,
	guard *agentResourceGuard,
	tenant, name string,
	args map[string]any,
) (string, error) {
	switch name {
	case "list_files":
		return a.callListFiles(ctx, guard, tenant, args)
	case "read_file":
		return a.callReadFile(ctx, guard, tenant, args)
	case "search":
		return a.callSearch(ctx, guard, tenant, args)
	default:
		return "error: unknown tool " + name, nil
	}
}

func (a *Agent) callListFiles(
	ctx context.Context,
	guard *agentResourceGuard,
	tenant string,
	args map[string]any,
) (string, error) {
	prefix, _ := args["prefix"].(string)
	limit := 20
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
	}
	if err := guard.admitToolBucket(ctx, tenant, service.DefaultBucket); err != nil {
		return "", err
	}
	page, err := a.svc.List(ctx, tenant, service.DefaultBucket, prefix, "", limit)
	if err != nil {
		if ordinaryAgentToolError(err) {
			return "error: " + err.Error(), nil
		}
		return "", err
	}
	guard.addList(tenant, service.DefaultBucket, page)
	var b strings.Builder
	for _, o := range page.Objects {
		fmt.Fprintf(&b, "%s\t%d bytes\n", o.Key, o.Size)
	}
	if b.Len() == 0 {
		return "(no objects)", nil
	}
	return b.String(), nil
}

func (a *Agent) callReadFile(
	ctx context.Context,
	guard *agentResourceGuard,
	tenant string,
	args map[string]any,
) (string, error) {
	key, _ := args["key"].(string)
	if key == "" {
		return "error: key required", nil
	}
	if err := guard.admitToolBucket(ctx, tenant, service.DefaultBucket); err != nil {
		return "", err
	}
	rc, obj, err := a.svc.Get(ctx, tenant, service.DefaultBucket, key)
	if err != nil {
		if ordinaryAgentToolError(err) {
			return "error: " + err.Error(), nil
		}
		return "", err
	}
	if nilLike(rc) {
		return "", fmt.Errorf("%w: missing object body", ErrAgentResourceChanged)
	}
	defer rc.Close()
	if err := guard.addReadObject(tenant, service.DefaultBucket, key, obj); err != nil {
		return "", err
	}
	if err := guard.validate(ctx); err != nil {
		return "", err
	}
	body, err := io.ReadAll(io.LimitReader(rc, 4<<10))
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return string(body), nil
}

func (a *Agent) callSearch(
	ctx context.Context,
	guard *agentResourceGuard,
	tenant string,
	args map[string]any,
) (string, error) {
	if a.search == nil {
		return "error: search not enabled", nil
	}
	q, _ := args["query"].(string)
	if q == "" {
		return "error: query required", nil
	}
	k := 5
	if v, ok := args["k"].(float64); ok {
		k = int(v)
	}
	if k <= 0 || k > 100 {
		k = 5
	}
	req := Request{Tenant: tenant, Query: q, K: k, Mode: "hybrid", Caller: "agent:search"}
	hits, err := a.search.Query(ctx, req)
	if err != nil {
		return "", err
	}
	guard.addSearch(req, hits)
	var b strings.Builder
	for i, h := range hits {
		fmt.Fprintf(&b, "[#%d] %s/%s#%d\n%s\n", i+1, h.Bucket, h.ObjectKey, h.Seq, h.Chunk)
	}
	return b.String(), nil
}

func decodeToolArgs(raw string) map[string]any {
	var args map[string]any
	_ = json.Unmarshal([]byte(raw), &args)
	return args
}
