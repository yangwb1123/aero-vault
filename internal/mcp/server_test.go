package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aero-vault/aero-vault/internal/ai"
	"github.com/aero-vault/aero-vault/internal/repository"
	"github.com/aero-vault/aero-vault/internal/service"
	"github.com/aero-vault/aero-vault/internal/storage"
)

// ---- helpers ----

// handle sends a single JSON-RPC message and decodes the response envelope.
func handle(t *testing.T, srv *Server, body string) rpcResponse {
	t.Helper()
	raw := srv.Handle(context.Background(), []byte(body))
	if raw == nil {
		t.Fatal("Handle returned nil for non-notification request")
	}
	var resp rpcResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal response: %v\n  raw: %s", err, raw)
	}
	return resp
}

// handleResult calls handle and returns the Result as a re-marshalled []byte.
func handleResult(t *testing.T, srv *Server, body string) []byte {
	t.Helper()
	resp := handle(t, srv, body)
	if resp.Error != nil {
		t.Fatalf("unexpected rpc error: code=%d msg=%s", resp.Error.Code, resp.Error.Message)
	}
	b, _ := json.Marshal(resp.Result)
	return b
}

// ---- initialize ----

func TestDispatch_Initialize(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	resp := handle(t, srv, body)

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	out, _ := json.Marshal(resp.Result)
	var init initializeResult
	if err := json.Unmarshal(out, &init); err != nil {
		t.Fatalf("decode initializeResult: %v", err)
	}
	if init.ProtocolVersion != ProtocolVersion {
		t.Errorf("ProtocolVersion = %q, want %q", init.ProtocolVersion, ProtocolVersion)
	}
	if name, _ := init.ServerInfo["name"].(string); name != "aero-vault" {
		t.Errorf("ServerInfo.name = %q, want aero-vault", name)
	}
	if init.Capabilities == nil {
		t.Error("Capabilities is nil")
	}
}

// ---- ping ----

func TestDispatch_Ping(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	resp := handle(t, srv, `{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if resp.Error != nil {
		t.Fatalf("ping error: %v", resp.Error)
	}
	if resp.Result == nil {
		t.Error("ping result should not be nil")
	}
}

// ---- notification (no ID) ----

func TestHandle_Notification_ReturnsNil(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	// A notification has no "id" field → Handle must return nil.
	raw := srv.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","method":"ping"}`))
	if raw != nil {
		t.Errorf("notification: want nil response, got %s", raw)
	}
}

// ---- invalid JSON / wrong version ----

func TestHandle_ParseError(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	raw := srv.Handle(context.Background(), []byte(`{not valid json`))
	var resp rpcResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal error response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != -32700 {
		t.Errorf("want parse-error -32700, got %+v", resp.Error)
	}
}

func TestHandle_InvalidJSONRPCVersion(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	raw := srv.Handle(context.Background(), []byte(`{"jsonrpc":"1.0","id":1,"method":"ping"}`))
	var resp rpcResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != -32600 {
		t.Errorf("want -32600 invalid jsonrpc, got %+v", resp.Error)
	}
}

func TestHandle_MissingJSONRPCVersion_Accepted(t *testing.T) {
	// The server's guard is `req.JSONRPC != "" && req.JSONRPC != "2.0"`, so an
	// absent field should still be dispatched (the empty string passes).
	srv, _, _ := newTestServer(t, nil)
	resp := handle(t, srv, `{"id":3,"method":"ping"}`)
	if resp.Error != nil {
		t.Fatalf("want success for absent jsonrpc field, got %+v", resp.Error)
	}
}

// ---- unknown method ----

func TestDispatch_UnknownMethod(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	resp := handle(t, srv, `{"jsonrpc":"2.0","id":99,"method":"does/not/exist"}`)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Errorf("want -32601 method not found, got %+v", resp.Error)
	}
}

// ---- tools/list ----

func TestListTools_NoSearch(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	raw := handleResult(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	var result listToolsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	// Without a search backend the search tool must NOT be listed.
	for _, n := range names {
		if n == "search" {
			t.Error("search tool listed despite nil search backend")
		}
	}
	// list_files and read_file must always be present.
	has := func(want string) bool {
		for _, n := range names {
			if n == want {
				return true
			}
		}
		return false
	}
	if !has("list_files") {
		t.Error("list_files tool missing")
	}
	if !has("read_file") {
		t.Error("read_file tool missing")
	}
}

func TestListTools_WithSearch(t *testing.T) {
	emb := ai.NewHashEmbedder(64)
	_, _, repo := newTestServer(t, nil) // throw-away, just need a live repo for ai.Search
	search := ai.NewSearch(repo, emb, nil)

	srv, _, _ := newTestServer(t, search)
	raw := handleResult(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)

	var result listToolsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, tool := range result.Tools {
		if tool.Name == "search" {
			return // found — pass
		}
	}
	t.Error("search tool not listed when search backend is configured")
}

// ---- tools/call: list_files ----

func TestCallTool_ListFiles_Empty(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	// No objects seeded — result should be an empty listing without error.
	raw := handleResult(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_files","arguments":{}}}`)

	var result toolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error in tool result: %s", result.Content[0].Text)
	}
	if len(result.Content) == 0 {
		t.Error("expected at least one content block")
	}
}

func TestCallTool_ListFiles_WithObjects(t *testing.T) {
	srv, svc, _ := newTestServer(t, nil)
	seedObject(t, svc, "default", "default", "alpha.txt", "content-a")
	seedObject(t, svc, "default", "default", "beta.txt", "content-b")

	raw := handleResult(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_files","arguments":{"bucket":"default"}}}`)

	var result toolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected isError: %s", result.Content[0].Text)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "alpha.txt") {
		t.Errorf("alpha.txt not in listing:\n%s", text)
	}
	if !strings.Contains(text, "beta.txt") {
		t.Errorf("beta.txt not in listing:\n%s", text)
	}
}

func TestCallTool_ListFiles_PrefixFilter(t *testing.T) {
	srv, svc, _ := newTestServer(t, nil)
	seedObject(t, svc, "default", "default", "img/a.png", "imgdata")
	seedObject(t, svc, "default", "default", "doc/b.txt", "docdata")

	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_files","arguments":{"prefix":"img/"}}}`
	raw := handleResult(t, srv, req)

	var result toolResult
	json.Unmarshal(raw, &result)
	if result.IsError {
		t.Fatalf("error: %s", result.Content[0].Text)
	}
	text := result.Content[0].Text
	if !strings.Contains(text, "img/a.png") {
		t.Errorf("expected img/a.png in result, got:\n%s", text)
	}
	if strings.Contains(text, "doc/b.txt") {
		t.Errorf("doc/b.txt should not appear for prefix img/:\n%s", text)
	}
}

// ---- tools/call: read_file ----

func TestCallTool_ReadFile_Success(t *testing.T) {
	srv, svc, _ := newTestServer(t, nil)
	seedObject(t, svc, "default", "default", "readme.txt", "hello world")

	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"bucket":"default","key":"readme.txt"}}}`
	raw := handleResult(t, srv, req)

	var result toolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected isError: %s", result.Content[0].Text)
	}
	if result.Content[0].Text != "hello world" {
		t.Errorf("body = %q, want %q", result.Content[0].Text, "hello world")
	}
}

func TestCallTool_ReadFile_MissingKey(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	// key argument absent → isError with "key required"
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"bucket":"default"}}}`
	raw := handleResult(t, srv, req)

	var result toolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.IsError {
		t.Error("expected isError=true for missing key")
	}
	if !strings.Contains(result.Content[0].Text, "key required") {
		t.Errorf("expected 'key required' in error text, got: %s", result.Content[0].Text)
	}
}

func TestCallTool_ReadFile_NotFound(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"key":"nonexistent.txt"}}}`
	raw := handleResult(t, srv, req)

	var result toolResult
	json.Unmarshal(raw, &result)
	if !result.IsError {
		t.Error("expected isError=true for missing object")
	}
}

// ---- tools/call: write_file ----

func TestCallTool_WriteFile_Success(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)

	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_file","arguments":{"key":"out.txt","content":"hello there"}}}`
	raw := handleResult(t, srv, req)

	var result toolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected isError: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "out.txt") {
		t.Errorf("expected key in write confirmation, got: %s", result.Content[0].Text)
	}

	// Read it back through the read_file tool to confirm it was persisted.
	readReq := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file","arguments":{"key":"out.txt"}}}`
	readRaw := handleResult(t, srv, readReq)
	var readResult toolResult
	if err := json.Unmarshal(readRaw, &readResult); err != nil {
		t.Fatalf("decode read: %v", err)
	}
	if readResult.IsError {
		t.Fatalf("read-back isError: %s", readResult.Content[0].Text)
	}
	if readResult.Content[0].Text != "hello there" {
		t.Errorf("read-back body = %q, want %q", readResult.Content[0].Text, "hello there")
	}
}

func TestCallTool_WriteFile_MissingKey(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	// key absent → isError with "key required"
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_file","arguments":{"content":"orphan"}}}`
	raw := handleResult(t, srv, req)

	var result toolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.IsError {
		t.Error("expected isError=true for missing key")
	}
	if !strings.Contains(result.Content[0].Text, "key required") {
		t.Errorf("expected 'key required', got: %s", result.Content[0].Text)
	}
}

func TestCallTool_WriteFile_MissingContent(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	// content absent (required by schema) → isError with "content required"
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_file","arguments":{"key":"empty.txt"}}}`
	raw := handleResult(t, srv, req)

	var result toolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.IsError {
		t.Error("expected isError=true for missing content")
	}
	if !strings.Contains(result.Content[0].Text, "content required") {
		t.Errorf("expected 'content required', got: %s", result.Content[0].Text)
	}
}

// ---- tools/call: delete_file ----

func TestCallTool_DeleteFile_Success(t *testing.T) {
	srv, svc, _ := newTestServer(t, nil)
	seedObject(t, svc, "default", "default", "gone.txt", "remove me")

	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_file","arguments":{"key":"gone.txt"}}}`
	raw := handleResult(t, srv, req)

	var result toolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected isError: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "gone.txt") {
		t.Errorf("expected key in delete confirmation, got: %s", result.Content[0].Text)
	}

	// A second read should now fail (object soft-deleted).
	readReq := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file","arguments":{"key":"gone.txt"}}}`
	readRaw := handleResult(t, srv, readReq)
	var readResult toolResult
	json.Unmarshal(readRaw, &readResult)
	if !readResult.IsError {
		t.Error("expected read of deleted object to error")
	}
}

func TestCallTool_DeleteFile_MissingKey(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_file","arguments":{}}}`
	raw := handleResult(t, srv, req)

	var result toolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.IsError {
		t.Error("expected isError=true for missing key")
	}
	if !strings.Contains(result.Content[0].Text, "key required") {
		t.Errorf("expected 'key required', got: %s", result.Content[0].Text)
	}
}

func TestCallTool_DeleteFile_NotFound(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"delete_file","arguments":{"key":"never-existed.txt"}}}`
	raw := handleResult(t, srv, req)

	var result toolResult
	json.Unmarshal(raw, &result)
	if !result.IsError {
		t.Error("expected isError=true for delete of nonexistent object")
	}
}

// ---- tools/call: unknown tool ----

func TestCallTool_UnknownTool(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	resp := handle(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"does_not_exist","arguments":{}}}`)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Errorf("want -32601 for unknown tool, got %+v", resp.Error)
	}
}

func TestCallTool_InvalidParams(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	// params is not a valid toolCallParams object
	resp := handle(t, srv, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":"not-an-object"}`)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Errorf("want -32602 invalid params, got %+v", resp.Error)
	}
}

// ---- tools/call: search (nil search) ----

func TestCallTool_Search_NilSearch(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"hello"}}}`
	raw := handleResult(t, srv, req)

	var result toolResult
	json.Unmarshal(raw, &result)
	if !result.IsError {
		t.Error("expected isError=true when search is nil")
	}
	if !strings.Contains(result.Content[0].Text, "search not enabled") {
		t.Errorf("expected 'search not enabled', got: %s", result.Content[0].Text)
	}
}

func TestCallTool_Search_EmptyQuery(t *testing.T) {
	emb := ai.NewHashEmbedder(64)
	_, _, repo := newTestServer(t, nil)
	search := ai.NewSearch(repo, emb, nil)
	srv, _, _ := newTestServer(t, search)

	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":""}}}`
	raw := handleResult(t, srv, req)

	var result toolResult
	json.Unmarshal(raw, &result)
	if !result.IsError {
		t.Error("expected isError=true for empty query")
	}
}

func TestCallTool_Search_WithResults(t *testing.T) {
	emb := ai.NewHashEmbedder(64)
	srv, svc, repo := newTestServer(t, nil)
	search := ai.NewSearch(repo, emb, nil)
	// Re-build server with real search on the same underlying repo.
	srv = NewServer(svc, repo, search, "default", nil)

	obj := seedObject(t, svc, "default", "default", "notes.txt", "quarterly revenue report")
	seedChunks(t, repo, obj, []string{"quarterly revenue report", "annual budget overview"}, emb)

	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"revenue","k":5}}}`
	raw := handleResult(t, srv, req)

	var result toolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
	// Result text should contain either a match or "(no matches)"
	if len(result.Content) == 0 {
		t.Error("empty content block")
	}
}

func TestCallTool_Search_NoMatches(t *testing.T) {
	emb := ai.NewHashEmbedder(64)
	srv, svc, repo := newTestServer(t, nil)
	search := ai.NewSearch(repo, emb, nil)
	srv = NewServer(svc, repo, search, "default", nil)

	// No chunks indexed → should return "(no matches)".
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"query":"completely unrelated query xyz"}}}`
	raw := handleResult(t, srv, req)

	var result toolResult
	json.Unmarshal(raw, &result)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "no matches") {
		t.Errorf("expected 'no matches' for empty index, got: %s", result.Content[0].Text)
	}
}

// ---- resources/list ----

func TestListResources_Empty(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	raw := handleResult(t, srv, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)

	var result listResourcesResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Resources == nil {
		t.Error("Resources should be an empty slice, not nil")
	}
}

func TestListResources_WithObjects(t *testing.T) {
	srv, svc, _ := newTestServer(t, nil)
	seedObject(t, svc, "default", "default", "file.txt", "data")

	raw := handleResult(t, srv, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)

	var result listResourcesResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result.Resources) == 0 {
		t.Fatal("expected at least one resource")
	}
	r := result.Resources[0]
	if !strings.HasPrefix(r.URI, "aero-vault://") {
		t.Errorf("URI %q does not start with aero-vault://", r.URI)
	}
	if r.Name != "file.txt" {
		t.Errorf("Name = %q, want file.txt", r.Name)
	}
}

// ---- resources/read ----

func TestReadResource_Success(t *testing.T) {
	srv, svc, _ := newTestServer(t, nil)
	seedObject(t, svc, "default", "default", "report.txt", "the report content")

	// Build the URI from the list response.
	raw := handleResult(t, srv, `{"jsonrpc":"2.0","id":1,"method":"resources/list"}`)
	var listResult listResourcesResult
	json.Unmarshal(raw, &listResult)
	if len(listResult.Resources) == 0 {
		t.Fatal("list returned no resources")
	}
	uri := listResult.Resources[0].URI

	readReq, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "resources/read",
		"params":  map[string]string{"uri": uri},
	})
	raw = handleResult(t, srv, string(readReq))

	var readResult readResourceResult
	if err := json.Unmarshal(raw, &readResult); err != nil {
		t.Fatalf("decode readResourceResult: %v", err)
	}
	if len(readResult.Contents) == 0 {
		t.Fatal("empty contents")
	}
	if readResult.Contents[0].Text != "the report content" {
		t.Errorf("text = %q, want %q", readResult.Contents[0].Text, "the report content")
	}
}

func TestReadResource_BadURIPrefix(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	req := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"s3://bucket/key"}}`
	resp := handle(t, srv, req)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Errorf("want -32602 for bad URI prefix, got %+v", resp.Error)
	}
}

func TestReadResource_ShortURI(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	// URI has prefix but only 1 path segment → should get -32602.
	req := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"aero-vault://tenant"}}`
	resp := handle(t, srv, req)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Errorf("want -32602 for short URI, got %+v", resp.Error)
	}
}

func TestReadResource_NotFound(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	req := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"aero-vault://default/default/missing.txt"}}`
	resp := handle(t, srv, req)
	if resp.Error == nil || resp.Error.Code != -32000 {
		t.Errorf("want -32000 for not-found resource, got %+v", resp.Error)
	}
}

func TestReadResource_InvalidParams(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	req := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":"not-an-object"}`
	resp := handle(t, srv, req)
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Errorf("want -32602 invalid params, got %+v", resp.Error)
	}
}

// ---- stringArg / intArg ----

func TestStringArg(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		def  string
		want string
	}{
		{"present string", map[string]any{"k": "v"}, "k", "default", "v"},
		{"missing key", map[string]any{}, "k", "default", "default"},
		{"wrong type int", map[string]any{"k": 42}, "k", "default", "default"},
		{"wrong type bool", map[string]any{"k": true}, "k", "fallback", "fallback"},
		{"empty string is valid", map[string]any{"k": ""}, "k", "x", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stringArg(tc.m, tc.key, tc.def)
			if got != tc.want {
				t.Errorf("stringArg = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIntArg(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]any
		key  string
		def  int
		want int
	}{
		{"float64 (JSON default)", map[string]any{"k": float64(7)}, "k", 0, 7},
		{"int type", map[string]any{"k": 3}, "k", 0, 3},
		{"int64 type", map[string]any{"k": int64(99)}, "k", 0, 99},
		{"missing key", map[string]any{}, "k", 42, 42},
		{"string type falls back", map[string]any{"k": "five"}, "k", 10, 10},
		{"bool type falls back", map[string]any{"k": true}, "k", 5, 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := intArg(tc.m, tc.key, tc.def)
			if got != tc.want {
				t.Errorf("intArg = %d, want %d", got, tc.want)
			}
		})
	}
}

// ---- NewServer defaults ----

func TestNewServer_Defaults(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	// logger nil → uses slog.Default(); tenant empty → falls back to "default"
	if srv == nil {
		t.Fatal("NewServer returned nil")
	}
	if srv.tenant != "default" {
		t.Errorf("default tenant = %q, want default", srv.tenant)
	}
}

func TestNewServer_EmptyTenantFallback(t *testing.T) {
	dir := t.TempDir()
	repo, err := repository.Open(context.Background(), "sqlite", "file:"+filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	repo.Migrate(context.Background())
	store, _ := storage.NewLocal(storage.LocalConfig{Root: filepath.Join(dir, "objects")})
	svc := service.NewFileService(store, repo, nil)

	srv := NewServer(svc, repo, nil, "", nil)
	if srv.tenant != "default" {
		t.Errorf("empty tenant should default to 'default', got %q", srv.tenant)
	}
}

// ---- ID passthrough ----

func TestHandle_IDPassthrough(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	// Use a string ID to verify it is echoed back verbatim.
	resp := handle(t, srv, `{"jsonrpc":"2.0","id":"req-abc","method":"ping"}`)
	if string(resp.ID) != `"req-abc"` {
		t.Errorf("ID = %s, want %q", resp.ID, `"req-abc"`)
	}
}

// ---- content block type ----

func TestCallTool_ReadFile_ContentBlockType(t *testing.T) {
	srv, svc, _ := newTestServer(t, nil)
	seedObject(t, svc, "default", "default", "block.txt", "body")

	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"key":"block.txt"}}}`
	raw := handleResult(t, srv, req)

	var result toolResult
	json.Unmarshal(raw, &result)
	if len(result.Content) == 0 {
		t.Fatal("no content blocks")
	}
	if result.Content[0].Type != "text" {
		t.Errorf("content block type = %q, want text", result.Content[0].Type)
	}
}

// ---- bytes.Buffer to avoid import-only compile ----

var _ = bytes.NewBuffer
