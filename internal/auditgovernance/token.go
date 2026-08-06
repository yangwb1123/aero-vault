package auditgovernance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yangwb1123/snaplink/interfaces/ssoclient"
	"github.com/yangwb1123/snaplink/interfaces/ssoclient/remote"
)

const maxTokenTTL = 24 * time.Hour

type tokenSource struct {
	client  credentialsClient
	now     func() time.Time
	mu      sync.Mutex
	token   string
	refresh time.Time
}

type credentialsClient interface {
	ClientCredentials(context.Context, ...string) (*ssoclient.TokenResponse, error)
}

func newTokenSource(
	tokenURL, clientID, secret string, client *http.Client,
) (*tokenSource, error) {
	endpoint, err := secureEndpoint(tokenURL)
	if err != nil || clientID == "" || secret == "" || client == nil {
		return nil, ErrInvalidConfig
	}
	sdkClient := *client
	sdkClient.Transport = &resourceTransport{base: client.Transport}
	tokens := remote.NewTokenClient(endpoint.String(),
		remote.WithClientCredentials(clientID, secret), remote.WithHTTPClient(&sdkClient))
	return &tokenSource{client: tokens, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *tokenSource) AccessToken(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && s.now().Before(s.refresh) {
		return s.token, nil
	}
	wire, err := s.fetch(ctx)
	if err != nil {
		return "", err
	}
	ttl := min(time.Duration(wire.ExpiresIn)*time.Second, maxTokenTTL)
	skew := min(30*time.Second, ttl/5)
	s.token, s.refresh = wire.AccessToken, s.now().Add(ttl-skew)
	return s.token, nil
}

func (s *tokenSource) fetch(ctx context.Context) (*ssoclient.TokenResponse, error) {
	wire, err := s.client.ClientCredentials(ctx, RequiredScope)
	if err != nil || !validTokenResponse(wire) || !validTokenScopes(wire.Scopes) {
		return nil, ErrTokenUnavailable
	}
	return wire, nil
}

// resourceTransport fills the one RFC 8707 gap in the pinned SDK. It can only
// add the fixed Audit Governance resource and bounds the response before the
// SDK decoder sees it.
type resourceTransport struct {
	base http.RoundTripper
}

func (t *resourceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	withResource, err := addResourceIndicator(request)
	if err != nil {
		return nil, ErrTokenUnavailable
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	response, err := base.RoundTrip(withResource)
	if err != nil {
		return nil, err
	}
	if err := bufferTokenResponse(response); err != nil {
		return nil, err
	}
	return response, nil
}

func addResourceIndicator(request *http.Request) (*http.Request, error) {
	if request == nil || request.Body == nil || request.Method != http.MethodPost {
		return nil, ErrTokenUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxResponseBytes+1))
	_ = request.Body.Close()
	if err != nil || len(body) > maxResponseBytes {
		return nil, ErrTokenUnavailable
	}
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, ErrTokenUnavailable
	}
	form.Set("resource", RequiredResource)
	encoded := form.Encode()
	clone := request.Clone(request.Context())
	clone.Body = io.NopCloser(strings.NewReader(encoded))
	clone.ContentLength = int64(len(encoded))
	clone.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(encoded)), nil
	}
	return clone, nil
}

func bufferTokenResponse(response *http.Response) error {
	if response == nil || response.Body == nil {
		return ErrTokenUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	_ = response.Body.Close()
	if err != nil || len(body) > maxResponseBytes {
		return ErrTokenUnavailable
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 &&
		(response.StatusCode != http.StatusOK ||
			!jsonMediaType(response.Header.Get("Content-Type")) || !json.Valid(body)) {
		return ErrTokenUnavailable
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	return nil
}

func jsonMediaType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}

func validTokenResponse(wire *ssoclient.TokenResponse) bool {
	return wire != nil && wire.AccessToken != "" &&
		!strings.ContainsAny(wire.AccessToken, "\r\n") &&
		strings.EqualFold(wire.TokenType, "Bearer") && wire.ExpiresIn > 0 &&
		wire.ExpiresIn <= int64(maxTokenTTL/time.Second)
}

func validTokenScopes(scopes []string) bool {
	return len(scopes) == 0 || len(scopes) == 1 && scopes[0] == RequiredScope
}

func (s *tokenSource) Invalidate(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == token {
		s.token, s.refresh = "", time.Time{}
	}
}
