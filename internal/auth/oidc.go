package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	oidcStateCookie = "aero_oidc_state"
	oidcFlowTTL     = 10 * time.Minute
)

// OIDCConfig describes a public Authorization Code + PKCE client.
type OIDCConfig struct {
	Issuer                string
	ClientID              string
	RedirectURI           string
	AuthorizationEndpoint string
	TokenEndpoint         string
	Scopes                []string
}

type pendingOIDCFlow struct {
	verifier string
	expires  time.Time
}

// OIDCHandler starts a browser login and exchanges the callback code. Tokens
// are returned in a URL fragment so they never enter access logs or referrers.
type OIDCHandler struct {
	cfg     OIDCConfig
	client  *http.Client
	mu      sync.Mutex
	pending map[string]pendingOIDCFlow
}

func NewOIDCHandler(cfg OIDCConfig) (*OIDCHandler, error) {
	cfg.Issuer = strings.TrimRight(cfg.Issuer, "/")
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.RedirectURI == "" {
		return nil, errors.New("oidc: issuer, client ID and redirect URI are required")
	}
	if cfg.AuthorizationEndpoint == "" {
		cfg.AuthorizationEndpoint = cfg.Issuer + "/auth/login"
	}
	if cfg.TokenEndpoint == "" {
		cfg.TokenEndpoint = cfg.Issuer + "/token"
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "profile", "email"}
	}
	if err := validateOIDCEndpoints(cfg); err != nil {
		return nil, err
	}
	return &OIDCHandler{
		cfg: cfg, client: &http.Client{Timeout: 15 * time.Second},
		pending: map[string]pendingOIDCFlow{},
	}, nil
}

func validateOIDCEndpoints(cfg OIDCConfig) error {
	for name, raw := range map[string]string{
		"issuer": cfg.Issuer, "redirect URI": cfg.RedirectURI,
		"authorization endpoint": cfg.AuthorizationEndpoint, "token endpoint": cfg.TokenEndpoint,
	} {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return fmt.Errorf("oidc: invalid %s", name)
		}
		if parsed.Scheme != "https" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" {
			return fmt.Errorf("oidc: %s must use https", name)
		}
	}
	return nil
}

func (h *OIDCHandler) Login(w http.ResponseWriter, r *http.Request) {
	state, err := randomURLToken(32)
	if err != nil {
		http.Error(w, "could not start login", http.StatusInternalServerError)
		return
	}
	verifier, err := randomURLToken(48)
	if err != nil {
		http.Error(w, "could not start login", http.StatusInternalServerError)
		return
	}
	h.storePending(state, verifier)
	h.setStateCookie(w, state, int(oidcFlowTTL.Seconds()))
	target, err := h.authorizationURL(state, verifier)
	if err != nil {
		http.Error(w, "invalid authorization endpoint", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (h *OIDCHandler) authorizationURL(state, verifier string) (string, error) {
	target, err := url.Parse(h.cfg.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}
	challenge := sha256.Sum256([]byte(verifier))
	query := target.Query()
	query.Set("client_id", h.cfg.ClientID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", h.cfg.RedirectURI)
	query.Set("scope", strings.Join(h.cfg.Scopes, " "))
	query.Set("state", state)
	query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	query.Set("code_challenge_method", "S256")
	target.RawQuery = query.Encode()
	return target.String(), nil
}

func (h *OIDCHandler) Callback(w http.ResponseWriter, r *http.Request) {
	h.noStore(w)
	if oidcErr := r.URL.Query().Get("error"); oidcErr != "" {
		http.Error(w, "identity provider rejected login", http.StatusUnauthorized)
		return
	}
	state, code := r.URL.Query().Get("state"), r.URL.Query().Get("code")
	cookie, _ := r.Cookie(oidcStateCookie)
	verifier, ok := h.consumePending(state, cookie)
	h.clearStateCookie(w)
	if !ok || code == "" {
		http.Error(w, "invalid or expired login state", http.StatusBadRequest)
		return
	}
	token, err := h.exchangeCode(r, code, verifier)
	if err != nil {
		http.Error(w, "OIDC token exchange failed", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, token.uiRedirect(), http.StatusFound)
}

type oidcTokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token,omitempty"`
	TokenType   string `json:"token_type,omitempty"`
	ExpiresIn   int64  `json:"expires_in,omitempty"`
}

func (h *OIDCHandler) exchangeCode(r *http.Request, code, verifier string) (oidcTokenResponse, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {h.cfg.ClientID},
		"code":          {code},
		"redirect_uri":  {h.cfg.RedirectURI},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, h.cfg.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return oidcTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := h.client.Do(req)
	if err != nil {
		return oidcTokenResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return oidcTokenResponse{}, fmt.Errorf("token endpoint returned HTTP %d", resp.StatusCode)
	}
	var token oidcTokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token); err != nil {
		return token, err
	}
	if token.AccessToken == "" {
		return token, errors.New("token endpoint omitted access_token")
	}
	return token, nil
}

func (t oidcTokenResponse) uiRedirect() string {
	fragment := url.Values{"oidc_access_token": {t.AccessToken}}
	if t.IDToken != "" {
		fragment.Set("oidc_id_token", t.IDToken)
	}
	if t.ExpiresIn > 0 {
		fragment.Set("oidc_expires_in", fmt.Sprint(t.ExpiresIn))
	}
	return "/ui#" + fragment.Encode()
}

func (h *OIDCHandler) Logout(w http.ResponseWriter, r *http.Request) {
	h.noStore(w)
	callback, _ := url.Parse(h.cfg.RedirectURI)
	postLogout := callback.Scheme + "://" + callback.Host + "/ui"
	target := h.cfg.Issuer + "/end_session?" + url.Values{
		"client_id":                {h.cfg.ClientID},
		"post_logout_redirect_uri": {postLogout},
	}.Encode()
	http.Redirect(w, r, target, http.StatusFound)
}

func (h *OIDCHandler) storePending(state, verifier string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	for key, flow := range h.pending {
		if now.After(flow.expires) {
			delete(h.pending, key)
		}
	}
	h.pending[state] = pendingOIDCFlow{verifier: verifier, expires: now.Add(oidcFlowTTL)}
}

func (h *OIDCHandler) consumePending(state string, cookie *http.Cookie) (string, bool) {
	if state == "" || cookie == nil || cookie.Value != state {
		return "", false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	flow, ok := h.pending[state]
	delete(h.pending, state)
	if !ok || time.Now().After(flow.expires) {
		return "", false
	}
	return flow.verifier, true
}

func (h *OIDCHandler) setStateCookie(w http.ResponseWriter, value string, maxAge int) {
	redirect, _ := url.Parse(h.cfg.RedirectURI)
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookie, Value: value, Path: "/auth/oidc",
		HttpOnly: true, Secure: redirect.Scheme == "https",
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
}

func (h *OIDCHandler) clearStateCookie(w http.ResponseWriter) {
	h.setStateCookie(w, "", -1)
}

func (h *OIDCHandler) noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

func randomURLToken(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
