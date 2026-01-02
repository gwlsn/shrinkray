package oidc

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gwlsn/shrinkray/internal/auth"
	"golang.org/x/oauth2"
)

const (
	defaultCookieName   = "shrinkray_session"
	defaultStateCookie  = "shrinkray_oidc_state"
	defaultStateTimeout = 10 * time.Minute
	defaultSessionTTL   = 24 * time.Hour
)

// Provider implements OIDC authentication with signed session cookies.
type Provider struct {
	oidcProvider    *oidc.Provider
	verifier        *oidc.IDTokenVerifier
	oauth2Config    *oauth2.Config
	secret          []byte
	cookieName      string
	stateCookieName string
	groupClaim      string
	allowedGroups   map[string]struct{}
	sessionTTL      time.Duration
}

// NewProvider initializes an OIDC auth provider.
func NewProvider(ctx context.Context, issuer, clientID, clientSecret, redirectURL string, scopes []string, groupClaim string, allowedGroups []string, secret string) (*Provider, error) {
	if issuer == "" {
		return nil, errors.New("oidc auth requires issuer")
	}
	if clientID == "" {
		return nil, errors.New("oidc auth requires client_id")
	}
	if clientSecret == "" {
		return nil, errors.New("oidc auth requires client_secret")
	}
	if redirectURL == "" {
		return nil, errors.New("oidc auth requires redirect_url")
	}
	if secret == "" {
		return nil, errors.New("oidc auth requires auth secret")
	}
	if len(allowedGroups) > 0 && groupClaim == "" {
		return nil, errors.New("oidc auth requires group_claim when allowed_groups is set")
	}

	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}

	oauthScopes := normalizeScopes(scopes)
	oauthConfig := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       oauthScopes,
		Endpoint:     provider.Endpoint(),
	}

	allowed := make(map[string]struct{}, len(allowedGroups))
	for _, group := range allowedGroups {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		allowed[group] = struct{}{}
	}

	return &Provider{
		oidcProvider:    provider,
		verifier:        provider.Verifier(&oidc.Config{ClientID: clientID}),
		oauth2Config:    oauthConfig,
		secret:          []byte(secret),
		cookieName:      defaultCookieName,
		stateCookieName: defaultStateCookie,
		groupClaim:      groupClaim,
		allowedGroups:   allowed,
		sessionTTL:      defaultSessionTTL,
	}, nil
}

// Authenticate validates the session cookie and returns the authenticated user.
func (p *Provider) Authenticate(r *http.Request) (*auth.User, error) {
	cookie, err := r.Cookie(p.cookieName)
	if err != nil {
		return nil, err
	}

	payload, err := p.verifySignedValue(cookie.Value)
	if err != nil {
		return nil, err
	}

	var session sessionPayload
	if err := json.Unmarshal(payload, &session); err != nil {
		return nil, err
	}
	expiry := time.Unix(session.ExpiresAt, 0)
	if expiry.Before(time.Now()) {
		return nil, errors.New("session expired")
	}
	return &auth.User{
		ID:    session.Subject,
		Email: session.Email,
		Name:  session.Name,
	}, nil
}

// LoginURL returns the login endpoint.
func (p *Provider) LoginURL(_ *http.Request) (string, error) {
	return "/auth/login", nil
}

// HandleLogin initiates the authorization code flow.
func (p *Provider) HandleLogin(w http.ResponseWriter, r *http.Request) error {
	state, err := generateNonce()
	if err != nil {
		return err
	}
	nonce, err := generateNonce()
	if err != nil {
		return err
	}
	expires := time.Now().Add(defaultStateTimeout)
	statePayload := statePayload{
		State:     state,
		Nonce:     nonce,
		ExpiresAt: expires.Unix(),
	}
	encoded, err := p.signStatePayload(statePayload)
	if err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     p.stateCookieName,
		Value:    encoded,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expires,
		Secure:   r.TLS != nil,
	})

	loginURL := p.oauth2Config.AuthCodeURL(state, oidc.Nonce(nonce))
	http.Redirect(w, r, loginURL, http.StatusFound)
	return nil
}

// HandleCallback validates the ID token and issues a session cookie.
func (p *Provider) HandleCallback(w http.ResponseWriter, r *http.Request) error {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		return errors.New("missing code or state")
	}

	cookie, err := r.Cookie(p.stateCookieName)
	if err != nil {
		return errors.New("missing auth state")
	}

	stateData, err := p.verifyStateCookie(cookie.Value)
	if err != nil {
		return err
	}
	if stateData.ExpiresAt < time.Now().Unix() {
		return errors.New("state expired")
	}
	if subtle.ConstantTimeCompare([]byte(state), []byte(stateData.State)) != 1 {
		return errors.New("invalid state")
	}

	clearStateCookie(w, p.stateCookieName)

	token, err := p.oauth2Config.Exchange(r.Context(), code)
	if err != nil {
		return err
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return errors.New("missing id_token")
	}

	idToken, err := p.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		return err
	}
	if idToken.Nonce != stateData.Nonce {
		return errors.New("invalid nonce")
	}

	var claims map[string]interface{}
	if err := idToken.Claims(&claims); err != nil {
		return err
	}

	subject, _ := claims["sub"].(string)
	email, _ := claims["email"].(string)
	name, _ := claims["name"].(string)

	if err := p.validateGroups(claims); err != nil {
		return err
	}

	expiry := idToken.Expiry
	if expiry.IsZero() {
		expiry = time.Now().Add(p.sessionTTL)
	}

	session := sessionPayload{
		Subject:   subject,
		Email:     email,
		Name:      name,
		ExpiresAt: expiry.Unix(),
	}
	encoded, err := p.signSessionPayload(session)
	if err != nil {
		return err
	}

	http.SetCookie(w, &http.Cookie{
		Name:     p.cookieName,
		Value:    encoded,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiry,
		Secure:   r.TLS != nil,
	})

	http.Redirect(w, r, "/", http.StatusFound)
	return nil
}

type statePayload struct {
	State     string `json:"state"`
	Nonce     string `json:"nonce"`
	ExpiresAt int64  `json:"expires_at"`
}

type sessionPayload struct {
	Subject   string `json:"sub"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	ExpiresAt int64  `json:"expires_at"`
}

func (p *Provider) signStatePayload(payload statePayload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return p.signValue(data), nil
}

func (p *Provider) verifyStateCookie(value string) (statePayload, error) {
	payload, err := p.verifySignedValue(value)
	if err != nil {
		return statePayload{}, err
	}
	var state statePayload
	if err := json.Unmarshal(payload, &state); err != nil {
		return statePayload{}, err
	}
	return state, nil
}

func (p *Provider) signSessionPayload(payload sessionPayload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return p.signValue(data), nil
}

func (p *Provider) signValue(payload []byte) string {
	signature := hmac.New(sha256.New, p.secret)
	signature.Write(payload)
	sum := signature.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sum)
}

func (p *Provider) verifySignedValue(value string) ([]byte, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return nil, errors.New("invalid session format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("invalid session payload")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid session signature")
	}
	expected := hmac.New(sha256.New, p.secret)
	expected.Write(payload)
	expectedSum := expected.Sum(nil)
	if subtle.ConstantTimeCompare(signature, expectedSum) != 1 {
		return nil, errors.New("invalid session signature")
	}
	return payload, nil
}

func (p *Provider) validateGroups(claims map[string]interface{}) error {
	if len(p.allowedGroups) == 0 {
		return nil
	}
	raw, ok := claims[p.groupClaim]
	if !ok {
		return fmt.Errorf("missing group claim: %s", p.groupClaim)
	}
	groups, err := extractGroups(raw)
	if err != nil {
		return err
	}
	for _, group := range groups {
		if _, ok := p.allowedGroups[group]; ok {
			return nil
		}
	}
	return errors.New("user is not in an allowed group")
}

func extractGroups(value interface{}) ([]string, error) {
	switch v := value.(type) {
	case string:
		if v == "" {
			return nil, nil
		}
		return []string{v}, nil
	case []string:
		return v, nil
	case []interface{}:
		groups := make([]string, 0, len(v))
		for _, item := range v {
			str, ok := item.(string)
			if !ok {
				return nil, errors.New("group claim contains non-string value")
			}
			if str == "" {
				continue
			}
			groups = append(groups, str)
		}
		return groups, nil
	default:
		return nil, errors.New("group claim has unsupported type")
	}
}

func normalizeScopes(scopes []string) []string {
	hasOpenID := false
	normalized := make([]string, 0, len(scopes)+1)
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if scope == oidc.ScopeOpenID {
			hasOpenID = true
		}
		normalized = append(normalized, scope)
	}
	if !hasOpenID {
		normalized = append([]string{oidc.ScopeOpenID}, normalized...)
	}
	if len(normalized) == 0 {
		return []string{oidc.ScopeOpenID, "profile", "email"}
	}
	return normalized
}

func generateNonce() (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

func clearStateCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
