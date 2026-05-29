package aerovault

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// errStopSSE is a sentinel a scanSSE callback returns to stop iteration early
// (e.g. after the "done" frame) without surfacing an error.
var errStopSSE = errors.New("aerovault: stop sse")

// errorsAs is a thin indirection over errors.As so client.go's AsError helper
// does not need its own errors import.
func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}

// scanSSE parses a text/event-stream body into (event, data) frames and invokes
// fn for each. Frames are separated by a blank line; consecutive "data:" lines
// accumulate joined by "\n"; an "event:" line names the frame (default
// "message"). Lines beginning with ":" are comments (keepalives) and skipped.
//
// If fn returns errStopSSE, scanning stops and scanSSE returns errStopSSE. Any
// other non-nil error from fn is propagated. This mirrors the Python client's
// _iter_sse.
func scanSSE(r io.Reader, fn func(event, data string) error) error {
	sc := bufio.NewScanner(r)
	// Allow long single-line data frames (e.g. the JSON "done" payload).
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	event := "message"
	var data []string

	dispatch := func() error {
		if len(data) == 0 {
			event = "message"
			return nil
		}
		err := fn(event, strings.Join(data, "\n"))
		event = "message"
		data = data[:0]
		return err
	}

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case line == "":
			if err := dispatch(); err != nil {
				return err
			}
		case strings.HasPrefix(line, ":"):
			// comment / keepalive — ignore
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	// Flush a trailing frame not terminated by a blank line.
	return dispatch()
}

// unquote returns the contents of a JSON-encoded string, or the trimmed input
// if it is not a quoted JSON string. Used for the SSE "error" frame, which the
// server writes with %q.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		var out string
		if json.Unmarshal([]byte(s), &out) == nil {
			return out
		}
		return s[1 : len(s)-1]
	}
	return s
}
