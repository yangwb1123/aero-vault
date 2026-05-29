package mcp

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"os"
)

// ServeStdio reads JSON-RPC messages from in (one object per line), dispatches
// them through s, and writes responses to out. It returns when ctx is canceled
// or in reaches EOF.
func ServeStdio(ctx context.Context, s *Server, in io.Reader, out io.Writer) error {
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		resp := s.Handle(ctx, line)
		if resp != nil {
			_, _ = out.Write(resp)
			_, _ = out.Write([]byte("\n"))
		}
	}
	return scanner.Err()
}

// HTTPHandler returns an http.Handler that accepts a single JSON-RPC request
// per POST. For Claude Desktop / Code, the stdio transport is preferred; HTTP
// is useful for browser-based clients and quick smoke testing.
func HTTPHandler(s *Server) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := s.Handle(r.Context(), body)
		w.Header().Set("Content-Type", "application/json")
		if resp == nil {
			// notification: 202 with empty body
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_, _ = w.Write(resp)
	})
}
