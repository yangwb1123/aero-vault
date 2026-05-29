// Package mcp implements a minimal Model Context Protocol server over JSON-RPC
// 2.0. Two transports are supported:
//
//   - stdio (one JSON object per line on os.Stdin / os.Stdout)
//   - HTTP   (POST JSON-RPC requests to /mcp)
//
// Exposed tools: list_files, read_file, search.
// Exposed resources: live objects in the active tenant.
//
// This is a hand-rolled MCP subset that talks to Claude Desktop, Claude Code,
// Cursor, Cline, and other JSON-RPC MCP clients. The full spec adds streaming
// and capabilities negotiation; both can be layered on the same primitives.
package mcp

import (
	"encoding/json"
)

const ProtocolVersion = "2024-11-05"

// JSON-RPC 2.0 wire types.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// MCP-specific shapes used in tools/resources responses.
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type toolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type listResourcesResult struct {
	Resources []resource `json:"resources"`
}

type readResourceResult struct {
	Contents []resourceContent `json:"contents"`
}

type resourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
}

type initializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	ServerInfo      map[string]any `json:"serverInfo"`
	Capabilities    map[string]any `json:"capabilities"`
}

type listToolsResult struct {
	Tools []tool `json:"tools"`
}
