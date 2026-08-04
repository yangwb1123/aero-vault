package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yangwb1123/snaplink/interfaces/ssoclient"
	"github.com/yangwb1123/snaplink/interfaces/ssoclient/remote"
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
	tokens  *remote.TokenClient
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
	tokens := remote.NewTokenClient(cfg.TokenEndpoint,
		remote.WithClientID(cfg.ClientID), remote.WithRedirectURI(cfg.RedirectURI),
		remote.WithPKCE(true), remote.WithHTTPClient(&http.Client{Timeout: 15 * time.Second}))
	return &OIDCHandler{cfg: cfg, tokens: tokens, pending: map[string]pendingOIDCFlow{}}, nil
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
	verifier, challenge, err := h.tokens.GeneratePKCE()
	if err != nil {
		http.Error(w, "could not start login", http.StatusInternalServerError)
		return
	}
	h.storePending(state, verifier)
	h.setStateCookie(w, state, int(oidcFlowTTL.Seconds()))
	target, err := h.authorizationURL(state, challenge)
	if err != nil {
		http.Error(w, "invalid authorization endpoint", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (h *OIDCHandler) authorizationURL(state, challenge string) (string, error) {
	target, err := url.Parse(h.cfg.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}
	query := target.Query()
	query.Set("client_id", h.cfg.ClientID)
	query.Set("response_type", "code")
	query.Set("redirect_uri", h.cfg.RedirectURI)
	query.Set("scope", strings.Join(h.cfg.Scopes, " "))
	query.Set("state", state)
	query.Set("code_challenge", challenge)
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
	token, err := h.tokens.ExchangeCode(r.Context(), code, verifier)
	if err != nil {
		http.Error(w, "OIDC token exchange failed", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, uiRedirect(token), http.StatusFound)
}

func uiRedirect(token *ssoclient.TokenResponse) string {
	fragment := url.Values{"oidc_access_token": {token.AccessToken}}
	if token.IDToken != "" {
		fragment.Set("oidc_id_token", token.IDToken)
	}
	if token.ExpiresIn > 0 {
		fragment.Set("oidc_expires_in", fmt.Sprint(token.ExpiresIn))
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
