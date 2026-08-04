package auth

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
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

// OIDCHandler starts a browser login and exchanges the callback code. Tokens
// are returned in a URL fragment so they never enter access logs or referrers.
type OIDCHandler struct {
	cfg  OIDCConfig
	flow *remote.BrowserFlow
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
		remote.WithAuthorizationEndpoint(cfg.AuthorizationEndpoint),
		remote.WithClientID(cfg.ClientID), remote.WithRedirectURI(cfg.RedirectURI),
		remote.WithPKCE(true), remote.WithHTTPClient(&http.Client{Timeout: 15 * time.Second}))
	flow, err := remote.NewBrowserFlow(tokens,
		remote.WithBrowserScopes(cfg.Scopes...),
		remote.WithBrowserCookie(oidcStateCookie, "/auth/oidc"),
		remote.WithBrowserFlowTTL(oidcFlowTTL))
	if err != nil {
		return nil, err
	}
	return &OIDCHandler{cfg: cfg, flow: flow}, nil
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
	if err := h.flow.Begin(w, r); err != nil {
		http.Error(w, "could not start login", http.StatusInternalServerError)
	}
}

func (h *OIDCHandler) Callback(w http.ResponseWriter, r *http.Request) {
	token, err := h.flow.Callback(w, r)
	if errors.Is(err, ssoclient.ErrAuthorizationRejected) {
		http.Error(w, "identity provider rejected login", http.StatusUnauthorized)
		return
	}
	if errors.Is(err, ssoclient.ErrInvalidAuthorizationFlow) {
		http.Error(w, "invalid or expired login state", http.StatusBadRequest)
		return
	}
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

func (h *OIDCHandler) noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
}
