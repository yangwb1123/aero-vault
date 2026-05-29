package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// RemoteExtractor sends binary content to an external service that returns
// plaintext. The protocol is intentionally simple so any of these can be
// plugged in without code changes:
//
//	POST {endpoint}/extract
//	Content-Type: multipart/form-data
//	  file=<binary>      content_type=<mime>
//	-> { "text": "...", "pages": [optional] }
//
// References for plug-in servers (any compatible adapter works):
//   - Apache Tika REST
//   - Unstructured.io API
//   - A homemade Python service wrapping pdfplumber / Whisper / PaddleOCR
//
// DefaultExtractor handles text/* in-process; RemoteExtractor wraps it,
// delegating only binary types to the remote service.
type RemoteExtractor struct {
	Endpoint string
	APIKey   string
	Fallback Extractor
	Client   *http.Client
}

func NewRemoteExtractor(endpoint, apiKey string, fallback Extractor) *RemoteExtractor {
	if endpoint == "" {
		return nil
	}
	if fallback == nil {
		fallback = NewDefaultExtractor()
	}
	return &RemoteExtractor{
		Endpoint: strings.TrimRight(endpoint, "/"),
		APIKey:   apiKey,
		Fallback: fallback,
		Client:   &http.Client{Timeout: 120 * time.Second},
	}
}

type remoteExtractResp struct {
	Text  string `json:"text"`
	Error string `json:"error,omitempty"`
}

func (e *RemoteExtractor) Extract(ctx context.Context, contentType string, r io.Reader) (string, error) {
	// Fast-path text content via the fallback extractor.
	ct := strings.ToLower(contentType)
	if strings.HasPrefix(ct, "text/") || ct == "" ||
		strings.HasPrefix(ct, "application/json") || strings.HasPrefix(ct, "application/xml") ||
		strings.HasPrefix(ct, "application/yaml") || strings.Contains(ct, "+xml") || strings.Contains(ct, "+json") {
		return e.Fallback.Extract(ctx, contentType, r)
	}
	// Otherwise stream through the remote.
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, err := mw.CreateFormFile("file", "object")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, r); err != nil {
		return "", err
	}
	_ = mw.WriteField("content_type", contentType)
	_ = mw.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.Endpoint+"/extract", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("remote extractor http %d: %s", resp.StatusCode, string(raw))
	}
	var out remoteExtractResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Error != "" {
		return "", fmt.Errorf("remote extractor: %s", out.Error)
	}
	return out.Text, nil
}
