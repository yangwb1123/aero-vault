package mcp

import (
	"bufio"
	"context"
	"encoding/json"
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
		// Every response on this endpoint is a JSON-RPC envelope, including the
		// error cases below, so advertise JSON up front.
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_, _ = w.Write(marshalErr(nil, -32600, "POST required"))
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write(marshalErr(nil, -32700, "read body: "+err.Error()))
			return
		}
		resp := s.Handle(r.Context(), body)
		if resp == nil {
			// notification: 202 with empty body
			w.WriteHeader(http.StatusAccepted)
			return
		}
		// If the JSON-RPC envelope carries an error, surface it as a matching
		// HTTP status instead of a blanket 200.
		if status := httpStatusForResponse(resp); status != http.StatusOK {
			w.WriteHeader(status)
		}
		_, _ = w.Write(resp)
	})
}

// httpStatusForResponse inspects a marshalled JSON-RPC response and maps its
// error code (if any) to an HTTP status. A response without an error — or one
// we cannot parse — stays 200, matching the success path.
func httpStatusForResponse(resp []byte) int {
	var env rpcResponse
	if err := json.Unmarshal(resp, &env); err != nil || env.Error == nil {
		return http.StatusOK
	}
	switch env.Error.Code {
	case -32700, -32600, -32602, -32601:
		return http.StatusBadRequest
	case -32000:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}
