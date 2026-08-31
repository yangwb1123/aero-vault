package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LLM is the chat-completion abstraction. The HTTPLLM client targets any
// OpenAI-compatible /v1/chat/completions endpoint (OpenAI, Anthropic via proxy,
// Ollama, vLLM, LocalAI, Azure OpenAI, Tongyi, etc.).
type LLM interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
	// ChatStream emits each token chunk through the callback. Returns the
	// final assembled response after the stream completes. Implementations
	// that don't support streaming should fall back to Chat + a single
	// callback with the full content.
	ChatStream(ctx context.Context, req ChatRequest, onChunk func(chunk string)) (ChatResponse, error)
	Name() string
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model       string        `json:"model,omitempty"`
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
	Tools       []ToolSpec    `json:"tools,omitempty"`
	ToolChoice  string        `json:"tool_choice,omitempty"` // "auto" | "none"
}

// ToolSpec describes a callable tool for OpenAI-style function calling.
type ToolSpec struct {
	Type     string         `json:"type"` // always "function"
	Function map[string]any `json:"function"`
}

// ToolCall is the LLM's request to invoke a tool.
type ToolCall struct {
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"`
	Function map[string]any `json:"function,omitempty"`
}

type ChatResponse struct {
	Content   string
	Model     string
	Usage     map[string]int
	ToolCalls []ToolCall
}

// HTTPLLM is the concrete client.
type HTTPLLM struct {
	Endpoint string
	Model    string
	APIKey   string
	Client   *http.Client
}

func NewHTTPLLM(endpoint, model, apiKey string) *HTTPLLM {
	if endpoint == "" {
		return nil
	}
	return &HTTPLLM{
		Endpoint: strings.TrimRight(endpoint, "/"),
		Model:    model,
		APIKey:   apiKey,
		Client:   &http.Client{Timeout: 90 * time.Second},
	}
}

func (l *HTTPLLM) Name() string { return l.Model }

type chatAPIResp struct {
	Choices []struct {
		Message struct {
			Role      string     `json:"role"`
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
	} `json:"choices"`
	Model string         `json:"model"`
	Usage map[string]int `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (l *HTTPLLM) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if req.Model == "" {
		req.Model = l.Model
	}
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, l.Endpoint+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if l.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+l.APIKey)
	}
	resp, err := l.Client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return ChatResponse{}, fmt.Errorf("llm http %d: %s", resp.StatusCode, string(raw))
	}
	var r chatAPIResp
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return ChatResponse{}, err
	}
	if r.Error != nil {
		return ChatResponse{}, errors.New(r.Error.Message)
	}
	if len(r.Choices) == 0 {
		return ChatResponse{}, errors.New("llm returned no choices")
	}
	return ChatResponse{
		Content:   r.Choices[0].Message.Content,
		Model:     r.Model,
		Usage:     r.Usage,
		ToolCalls: r.Choices[0].Message.ToolCalls,
	}, nil
}

// streamChunk mirrors the OpenAI SSE delta shape: each `data:` line carries a
// JSON object with `choices[].delta.{role,content,tool_calls}`.
type streamChunk struct {
	Choices []struct {
		Delta struct {
			Role      string     `json:"role,omitempty"`
			Content   string     `json:"content,omitempty"`
			ToolCalls []ToolCall `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason,omitempty"`
	} `json:"choices"`
	Model string          `json:"model,omitempty"`
	Usage json.RawMessage `json:"usage,omitempty"`
}

type parsedStreamLine struct {
	token    string
	model    string
	usage    map[string]int
	hasUsage bool
}

// ChatStream POSTs with "stream":true and parses the SSE response (one
// JSON object per `data:` line, terminated by `data: [DONE]`).
func (l *HTTPLLM) ChatStream(ctx context.Context, req ChatRequest, onChunk func(string)) (ChatResponse, error) {
	if req.Model == "" {
		req.Model = l.Model
	}
	req.Stream = true
	httpReq, err := l.buildChatRequest(ctx, req)
	if err != nil {
		return ChatResponse{}, err
	}
	resp, err := l.Client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return ChatResponse{}, fmt.Errorf("llm stream http %d: %s", resp.StatusCode, string(raw))
	}
	var content strings.Builder
	responseModel := req.Model
	var usage map[string]int
	scanner := newSSEScanner(resp.Body)
	for scanner.Scan() {
		parsed, done, err := parseSSELine(scanner.Text())
		if err != nil {
			continue
		}
		if done {
			break
		}
		if parsed.model != "" {
			responseModel = parsed.model
		}
		if parsed.hasUsage {
			usage = parsed.usage
		}
		if parsed.token != "" {
			content.WriteString(parsed.token)
			if onChunk != nil {
				onChunk(parsed.token)
			}
		}
	}
	return ChatResponse{Content: content.String(), Model: responseModel, Usage: usage}, scanner.Err()
}

func parseSSELine(line string) (parsedStreamLine, bool, error) {
	if line == "[DONE]" {
		return parsedStreamLine{}, true, nil
	}
	var ch streamChunk
	if err := json.Unmarshal([]byte(line), &ch); err != nil {
		return parsedStreamLine{}, false, err
	}
	var token string
	for _, c := range ch.Choices {
		if c.Delta.Content != "" {
			token += c.Delta.Content
		}
	}
	usage, hasUsage := decodeStreamUsage(ch.Usage)
	return parsedStreamLine{
		token: token, model: ch.Model, usage: usage, hasUsage: hasUsage,
	}, false, nil
}

func decodeStreamUsage(raw json.RawMessage) (map[string]int, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil || fields == nil {
		return nil, false
	}
	usage := make(map[string]int, len(fields))
	for name, value := range fields {
		value = bytes.TrimSpace(value)
		if len(value) == 0 || bytes.Equal(value, []byte("null")) {
			continue
		}
		var count int
		if err := json.Unmarshal(value, &count); err == nil {
			usage[name] = count
		}
	}
	return usage, true
}

func (l *HTTPLLM) buildChatRequest(ctx context.Context, req ChatRequest) (*http.Request, error) {
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, l.Endpoint+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if l.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+l.APIKey)
	}
	return httpReq, nil
}

// newSSEScanner returns an SSE line scanner that yields the JSON body of each
// `data: …` line (stripping the prefix). Empty lines and `data: [DONE]`
// terminators are emitted as well so the caller can detect end-of-stream.
type sseScanner struct {
	r   *bufio.Reader
	buf string
	err error
}

func newSSEScanner(r io.Reader) *sseScanner { return &sseScanner{r: bufio.NewReader(r)} }

func (s *sseScanner) Scan() bool {
	for {
		line, err := s.r.ReadString('\n')
		if err != nil && line == "" {
			s.err = err
			if err == io.EOF {
				s.err = nil
			}
			return false
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		const prefix = "data:"
		if strings.HasPrefix(line, prefix) {
			s.buf = strings.TrimSpace(line[len(prefix):])
			return true
		}
	}
}

func (s *sseScanner) Text() string { return s.buf }
func (s *sseScanner) Err() error   { return s.err }

// Chat fallback path for MockLLM: yields one chunk and returns.
func (MockLLM) ChatStream(ctx context.Context, req ChatRequest, onChunk func(string)) (ChatResponse, error) {
	resp, err := MockLLM{}.Chat(ctx, req)
	if err != nil {
		return resp, err
	}
	if onChunk != nil {
		// emit token-by-token so SSE tests see multiple frames
		for _, w := range strings.Fields(resp.Content) {
			onChunk(w + " ")
		}
	}
	return resp, nil
}

// MockLLM returns a canned response that includes the prompt — for offline
// smoke tests and the demo when no real LLM is configured.
type MockLLM struct{}

func (MockLLM) Name() string { return "mock" }
func (MockLLM) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	var b strings.Builder
	b.WriteString("[mock-llm] received ")
	b.WriteString(fmt.Sprintf("%d msg(s). ", len(req.Messages)))
	if len(req.Messages) > 0 {
		last := req.Messages[len(req.Messages)-1]
		b.WriteString("Last user question: ")
		b.WriteString(strings.TrimSpace(last.Content))
	}
	return ChatResponse{Content: b.String(), Model: "mock"}, nil
}
