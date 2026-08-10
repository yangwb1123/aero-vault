package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"
)

// maxErrorLine bounds the operator-facing error line rendered from a raw
// (non-envelope) response body so a proxy HTML / plain-text dump can never
// flood stderr (design F-A).
const maxErrorLine = 512

// apiErrorBody mirrors the REST error envelope (rest/dto.go, docs/api.md):
// {"error":{"code","message","request_id"}}.
type apiErrorBody struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

// renderError consumes resp.Body (callers must not read it afterwards) and
// returns a single-line, operator-readable error text. Rendering rules, in
// order:
//
//  1. body parses as the REST error envelope with code/message at least one
//     non-empty → "HTTP <status> <code>: <message>"; the "<code> " segment is
//     omitted when code is empty (no double space); a non-empty request_id
//     appends " (request <id>)".
//  2. body is non-empty but not an envelope (proxy HTML, plain text, JSON
//     without error fields) → "HTTP <status>: <raw>", where whitespace runs
//     collapse to single spaces and the whole rendered line is truncated to
//     maxErrorLine bytes (rune-safe, "…" suffix).
//  3. body is empty or unreadable → "HTTP <status>".
//
// It never panics: parse/read failures degrade to rules 2/3.
func renderError(resp *http.Response) string {
	status := resp.StatusCode
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Sprintf("HTTP %d", status)
	}
	if len(body) == 0 {
		return fmt.Sprintf("HTTP %d", status)
	}
	var env apiErrorBody
	if json.Unmarshal(body, &env) == nil && (env.Error.Code != "" || env.Error.Message != "") {
		line := fmt.Sprintf("HTTP %d %s: %s", status, env.Error.Code, env.Error.Message)
		if env.Error.Code == "" {
			line = fmt.Sprintf("HTTP %d: %s", status, env.Error.Message)
		}
		if env.Error.RequestID != "" {
			line += " (request " + env.Error.RequestID + ")"
		}
		return line
	}
	return boundedRawLine(fmt.Sprintf("HTTP %d: ", status), string(body))
}

// boundedRawLine renders prefix+raw as one line of at most maxErrorLine
// bytes: whitespace runs collapse to a single space (trimmed), and when the
// line would overflow, the raw part is truncated at a rune boundary with a
// "…" suffix.
func boundedRawLine(prefix, raw string) string {
	collapsed := strings.Join(strings.Fields(raw), " ")
	if len(prefix)+len(collapsed) <= maxErrorLine {
		return prefix + collapsed
	}
	room := maxErrorLine - len(prefix) - len("…")
	truncated := collapsed
	for len(truncated) > room {
		_, size := utf8.DecodeLastRuneInString(truncated)
		truncated = truncated[:len(truncated)-size]
	}
	return prefix + truncated + "…"
}

func readSuccessfulResponse(resp *http.Response) ([]byte, bool) {
	if resp.StatusCode >= http.StatusMultipleChoices {
		fmt.Fprintln(os.Stderr, renderError(resp))
		return nil, false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read response:", err)
		return nil, false
	}
	return body, true
}

func printResponseBody(body []byte) {
	if len(body) > 0 {
		fmt.Println(string(body))
	}
}
