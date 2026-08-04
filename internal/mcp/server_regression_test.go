package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mw "github.com/aero-vault/aero-vault/internal/middleware"
)

func TestTenantForHonorsExplicitDefaultContext(t *testing.T) {
	server, _, _ := newTestServer(t, nil)
	server.tenant = "stdio-tenant"
	var requestContext context.Context
	handler := mw.Tenant(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		requestContext = request.Context()
	}))
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set(mw.TenantHeader, "default")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if got := server.tenantFor(requestContext); got != "default" {
		t.Fatalf("tenant=%q want explicit default", got)
	}
}

func TestWriteFileAllowsEmptyContent(t *testing.T) {
	server, service, _ := newTestServer(t, nil)
	raw := handleResult(t, server, `{
		"jsonrpc":"2.0","id":1,"method":"tools/call",
		"params":{"name":"write_file","arguments":{"key":"empty.txt","content":""}}
	}`)
	var result toolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.IsError {
		t.Fatalf("write failed: %s", result.Content[0].Text)
	}
	object, err := service.Stat(context.Background(), "", "", "empty.txt")
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if object.Size != 0 {
		t.Fatalf("size=%d want 0", object.Size)
	}
}

func TestReadFileRejectsRatherThanTruncatesLargeText(t *testing.T) {
	server, service, _ := newTestServer(t, nil)
	seedObject(t, service, "default", "default", "large.txt", strings.Repeat("x", (4<<20)+1))
	raw := handleResult(t, server, `{
		"jsonrpc":"2.0","id":1,"method":"tools/call",
		"params":{"name":"read_file","arguments":{"key":"large.txt"}}
	}`)
	var result toolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "exceeds 4 MiB") {
		t.Fatalf("result=%+v", result)
	}
}
