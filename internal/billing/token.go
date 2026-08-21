package billing

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/yangwb1123/snaplink/interfaces/ssoclient"
	"github.com/yangwb1123/snaplink/interfaces/ssoclient/remote"
)

type credentialsClient interface {
	ClientCredentials(ctx context.Context, scopes ...string) (*ssoclient.TokenResponse, error)
}

type tokenSource struct {
	mu      sync.Mutex
	client  credentialsClient
	token   string
	expires time.Time
	now     func() time.Time
}

func newTokenSource(
	tokenURL, clientID, secret string, httpClient *http.Client,
) *tokenSource {
	client := remote.NewTokenClient(tokenURL,
		remote.WithClientCredentials(clientID, secret),
		remote.WithHTTPClient(httpClient))
	return &tokenSource{client: client, now: time.Now}
}

func (s *tokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	if s.token != "" && now.Before(s.expires) {
		return s.token, nil
	}
	response, err := s.client.ClientCredentials(ctx, ScopeEntitlementRead, ScopeMeteringWrite)
	if err != nil {
		return "", err
	}
	if response == nil || response.AccessToken == "" {
		return "", errors.New("snaplink billing token response is empty")
	}
	ttl := time.Duration(response.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Minute
	}
	skew := 30 * time.Second
	if ttl <= skew {
		skew = ttl / 5
	}
	s.token = response.AccessToken
	s.expires = now.Add(ttl - skew)
	return s.token, nil
}

func (s *tokenSource) Invalidate(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == token {
		s.token = ""
		s.expires = time.Time{}
	}
}
