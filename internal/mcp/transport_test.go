package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---- HTTPHandler ----

func TestHTTPHandler_POST_Initialize(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	h := HTTPHandler(srv)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var resp rpcResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v\n  body: %s", err, rec.Body.String())
	}
	if resp.Error != nil {
		t.Fatalf("rpc error: %+v", resp.Error)
	}
}

func TestHTTPHandler_POST_ToolsList(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	h := HTTPHandler(srv)

	body := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp rpcResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Error != nil {
		t.Fatalf("rpc error: %+v", resp.Error)
	}
}

func TestHTTPHandler_GET_MethodNotAllowed(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	h := HTTPHandler(srv)

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
}

func TestHTTPHandler_Notification_202(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	h := HTTPHandler(srv)

	// A JSON-RPC notification (no "id") → Handle returns nil → 202 empty body.
	body := `{"jsonrpc":"2.0","method":"ping"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Errorf("notification status = %d, want 202", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("notification body should be empty, got %q", rec.Body.String())
	}
}

func TestHTTPHandler_PUT_MethodNotAllowed(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	h := HTTPHandler(srv)

	req := httptest.NewRequest(http.MethodPut, "/mcp", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT status = %d, want 405", rec.Code)
	}
}

// Verify the JSON-RPC envelope shape including the "id" echo.
func TestHTTPHandler_IDEcho(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)
	h := HTTPHandler(srv)

	body := `{"jsonrpc":"2.0","id":42,"method":"ping"}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var resp rpcResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if string(resp.ID) != "42" {
		t.Errorf("id echo = %s, want 42", resp.ID)
	}
}

// Full round-trip through httptest.Server (not httptest.Recorder).
func TestHTTPHandler_RoundTrip(t *testing.T) {
	srv, svc, _ := newTestServer(t, nil)
	seedObject(t, svc, "default", "default", "hello.txt", "world")

	ts := httptest.NewServer(HTTPHandler(srv))
	t.Cleanup(ts.Close)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_files","arguments":{}}}`
	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	var rpcResp rpcResponse
	if err := json.Unmarshal(b, &rpcResp); err != nil {
		t.Fatalf("decode: %v\n  body: %s", err, b)
	}
	if rpcResp.Error != nil {
		t.Fatalf("rpc error: %+v", rpcResp.Error)
	}
	// Unpack tool result and check the file appears.
	out, _ := json.Marshal(rpcResp.Result)
	var result toolResult
	json.Unmarshal(out, &result)
	if result.IsError {
		t.Errorf("unexpected isError: %s", result.Content[0].Text)
	}
	if !strings.Contains(result.Content[0].Text, "hello.txt") {
		t.Errorf("expected hello.txt in listing, got: %s", result.Content[0].Text)
	}
}

// ---- ServeStdio ----

func TestServeStdio_SingleRequest(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)

	input := `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"
	in := strings.NewReader(input)
	var out bytes.Buffer

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := ServeStdio(ctx, srv, in, &out); err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}
	output := out.String()
	if !strings.HasSuffix(output, "\n") {
		t.Error("output should be newline-terminated")
	}
	// Strip trailing newline and decode.
	line := strings.TrimRight(output, "\n")
	var resp rpcResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal: %v\n  line: %q", err, line)
	}
	if resp.Error != nil {
		t.Fatalf("rpc error: %+v", resp.Error)
	}
	if string(resp.ID) != "1" {
		t.Errorf("id = %s, want 1", resp.ID)
	}
}

func TestServeStdio_MultipleRequests(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)

	lines := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":2,"method":"initialize"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	ctx := context.Background()
	if err := ServeStdio(ctx, srv, strings.NewReader(lines), &out); err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}

	responses := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(responses) != 3 {
		t.Fatalf("expected 3 response lines, got %d:\n%s", len(responses), out.String())
	}
	for i, line := range responses {
		var resp rpcResponse
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Errorf("line %d unmarshal: %v\n  %q", i, err, line)
			continue
		}
		if resp.Error != nil {
			t.Errorf("line %d unexpected error: %+v", i, resp.Error)
		}
	}
}

func TestServeStdio_Notification_NoOutputLine(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)

	// One notification (no id) followed by one real request.
	input := `{"jsonrpc":"2.0","method":"ping"}` + "\n" +
		`{"jsonrpc":"2.0","id":5,"method":"ping"}` + "\n"

	var out bytes.Buffer
	ServeStdio(context.Background(), srv, strings.NewReader(input), &out)

	// Only the real request produces output; the notification is silent.
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 output line for 1 request + 1 notification, got %d:\n%s", len(lines), out.String())
	}
}

func TestServeStdio_EmptyLines_Skipped(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)

	// Blank lines between real requests must be silently ignored.
	input := "\n" + `{"jsonrpc":"2.0","id":7,"method":"ping"}` + "\n\n"
	var out bytes.Buffer
	ServeStdio(context.Background(), srv, strings.NewReader(input), &out)

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 output line, got %d:\n%s", len(lines), out.String())
	}
}

func TestServeStdio_ContextCancel(t *testing.T) {
	srv, _, _ := newTestServer(t, nil)

	// A pipe gives us a reader that blocks once the buffer is drained.
	pr, pw := io.Pipe()
	var out bytes.Buffer

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- ServeStdio(ctx, srv, pr, &out)
	}()

	// Send one request, then cancel context, then close writer.
	pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n"))
	cancel()
	pw.Close()

	err := <-done
	// Either context.Canceled or nil (if scanner drained before cancel was seen).
	if err != nil && err != context.Canceled {
		t.Errorf("ServeStdio returned unexpected error: %v", err)
	}
}

func TestServeStdio_NilReadersUseDefaults(t *testing.T) {
	// Passing nil for in/out should not panic (it will use os.Stdin/os.Stdout).
	// We can't actually exercise the default paths without replacing os.Stdin, so
	// just verify the function handles EOF immediately from a real empty reader.
	srv, _, _ := newTestServer(t, nil)
	if err := ServeStdio(context.Background(), srv, strings.NewReader(""), nil); err != nil {
		// nil out is replaced by os.Stdout; empty input causes immediate EOF → nil.
		t.Fatalf("empty input with nil out: %v", err)
	}
}
