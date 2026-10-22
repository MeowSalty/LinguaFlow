// Package authoidc contains the deliberately small OIDC adapter used by the
// account authentication service. It owns all provider/network validation;
// callers only receive a stable, provider-independent identity result.
package authoidc

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authhttp"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authprotocol"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

var (
	ErrInvalidConfig = authprotocol.ErrInvalidConfig
	ErrUnavailable   = authprotocol.ErrUnavailable
	ErrInvalidToken  = authprotocol.ErrInvalidToken
	ErrFreshAuth     = authprotocol.ErrFreshAuth
)

type Config struct {
	Issuer           string
	ClientID         string
	ClientSecret     string
	ClientAuthMethod string
	Scopes           []string
	UserInfoEnabled  bool
}

type Identity = authprotocol.Identity
type ProviderMetadata = authprotocol.ProviderMetadata
type AuthorizationRequest = authprotocol.AuthorizationRequest
type Authorization = authprotocol.Authorization
type ExchangeRequest = authprotocol.ExchangeRequest

type Client struct {
	deployment config.AuthDeploymentConfig
	transport  *authhttp.Transport
}

func New(deployment config.AuthDeploymentConfig) (*Client, error) {
	if deployment.RequestTimeout == 0 {
		deployment.RequestTimeout = 10 * time.Second
	}
	if deployment.MaxResponseBytes == 0 {
		deployment.MaxResponseBytes = 1 << 20
	}
	if deployment.RateLimitPerMinute == 0 {
		deployment.RateLimitPerMinute = 60
	}
	if err := validateDeployment(deployment); err != nil {
		return nil, err
	}
	transport := authhttp.New(deployment)
	return &Client{deployment: deployment, transport: transport}, nil
}

func validateDeployment(d config.AuthDeploymentConfig) error {
	if d.RequestTimeout <= 0 || d.MaxResponseBytes <= 0 || d.RateLimitPerMinute <= 0 {
		return ErrInvalidConfig
	}
	for _, raw := range append(append([]string{}, d.CompletionURLs...), d.PublicBaseURL) {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Fragment != "" {
			return ErrInvalidConfig
		}
		if u.Scheme != "https" && !(d.AllowHTTPLoopback && u.Scheme == "http" && authhttp.IsLoopbackHost(u.Hostname())) {
			return ErrInvalidConfig
		}
	}
	return nil
}

func (c *Client) validateConnection(connection Config) error {
	u, err := url.Parse(strings.TrimSpace(connection.Issuer))
	if err != nil || !u.IsAbs() || u.Host == "" || u.User != nil || u.Fragment != "" || connection.ClientID == "" || !c.transport.ValidURL(u) {
		return ErrInvalidConfig
	}
	if connection.ClientAuthMethod == "" {
		connection.ClientAuthMethod = "basic"
	}
	if connection.ClientAuthMethod != "basic" && connection.ClientAuthMethod != "post" {
		return ErrInvalidConfig
	}
	return nil
}

func (c *Client) httpClient() *http.Client {
	return &http.Client{
		Transport:     c.transport,
		Timeout:       c.deployment.RequestTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (c *Client) provider(ctx context.Context, cfg Config) (*oidc.Provider, error) {
	if err := c.validateConnection(cfg); err != nil {
		return nil, err
	}
	hc := c.httpClient()
	ctx = oidc.ClientContext(ctx, hc)
	p, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, ErrUnavailable
	}
	return p, nil
}

func (c *Client) Check(ctx context.Context, cfg Config) (ProviderMetadata, error) {
	p, err := c.provider(ctx, cfg)
	if err != nil {
		return ProviderMetadata{}, err
	}
	e := p.Endpoint()
	meta := ProviderMetadata{Issuer: cfg.Issuer, AuthorizationEndpoint: e.AuthURL, TokenEndpoint: e.TokenURL, AuthStyle: normalizedAuthMethod(cfg)}
	if !c.transport.ValidRawURL(e.AuthURL) || !c.transport.ValidRawURL(e.TokenURL) {
		return ProviderMetadata{}, ErrUnavailable
	}
	if p.UserInfoEndpoint() != "" {
		if !c.transport.ValidRawURL(p.UserInfoEndpoint()) {
			return ProviderMetadata{}, ErrUnavailable
		}
		meta.UserInfoEndpoint = p.UserInfoEndpoint()
	}
	var claims struct {
		ScopesSupported                   []string `json:"scopes_supported"`
		CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
		TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	}
	if err := p.Claims(&claims); err != nil {
		return ProviderMetadata{}, ErrUnavailable
	}
	meta.ScopesSupported = append([]string(nil), claims.ScopesSupported...)
	for _, method := range claims.CodeChallengeMethodsSupported {
		if method == "S256" {
			meta.SupportsPKCES256 = true
		}
	}
	if !meta.SupportsPKCES256 {
		return ProviderMetadata{}, ErrInvalidConfig
	}
	wantAuth := "client_secret_basic"
	if meta.AuthStyle == "post" {
		wantAuth = "client_secret_post"
	}
	if len(claims.TokenEndpointAuthMethodsSupported) > 0 {
		supported := false
		for _, method := range claims.TokenEndpointAuthMethodsSupported {
			if method == wantAuth {
				supported = true
			}
		}
		if !supported {
			return ProviderMetadata{}, ErrInvalidConfig
		}
	}
	return meta, nil
}

func (c *Client) BuildAuthorization(ctx context.Context, cfg Config, req AuthorizationRequest) (Authorization, error) {
	if err := c.validateConnection(cfg); err != nil || req.State == "" || req.Nonce == "" || !validCodeVerifier(req.CodeVerifier) || !c.validRedirect(req.RedirectURL) {
		return Authorization{}, ErrInvalidConfig
	}
	p, err := c.provider(ctx, cfg)
	if err != nil {
		return Authorization{}, err
	}
	if !c.transport.ValidRawURL(p.Endpoint().AuthURL) {
		return Authorization{}, ErrUnavailable
	}
	challenge := pkceChallenge(req.CodeVerifier)
	scopes := normalizedScopes(cfg.Scopes)
	oauthCfg := oauth2.Config{ClientID: cfg.ClientID, Endpoint: p.Endpoint(), RedirectURL: req.RedirectURL, Scopes: scopes}
	params := []oauth2.AuthCodeOption{oauth2.SetAuthURLParam("nonce", req.Nonce), oauth2.SetAuthURLParam("code_challenge", challenge), oauth2.SetAuthURLParam("code_challenge_method", "S256")}
	if req.Fresh {
		maxAge := req.MaxAge
		if maxAge < 0 {
			maxAge = 0
		}
		params = append(params, oauth2.SetAuthURLParam("max_age", fmt.Sprintf("%d", int64(maxAge/time.Second))))
	}
	return Authorization{URL: oauthCfg.AuthCodeURL(req.State, params...), CodeChallenge: challenge}, nil
}

func (c *Client) ExchangeAndVerify(ctx context.Context, cfg Config, req ExchangeRequest) (Identity, error) {
	if err := c.validateConnection(cfg); err != nil || req.Code == "" || !validCodeVerifier(req.CodeVerifier) || !c.validRedirect(req.RedirectURL) {
		return Identity{}, ErrInvalidConfig
	}
	ctx = oidc.ClientContext(ctx, c.httpClient())
	p, err := c.provider(ctx, cfg)
	if err != nil {
		return Identity{}, err
	}
	oauthCfg := oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: p.Endpoint(), RedirectURL: req.RedirectURL, Scopes: normalizedScopes(cfg.Scopes)}
	if normalizedAuthMethod(cfg) == "post" {
		oauthCfg.Endpoint.AuthStyle = oauth2.AuthStyleInParams
	} else {
		oauthCfg.Endpoint.AuthStyle = oauth2.AuthStyleInHeader
	}
	tok, err := oauthCfg.Exchange(ctx, req.Code, oauth2.SetAuthURLParam("code_verifier", req.CodeVerifier))
	if err != nil {
		var upstream *oauth2.RetrieveError
		if errors.Is(err, ErrUnavailable) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
			(errors.As(err, &upstream) && upstream.Response != nil && (upstream.Response.StatusCode >= 500 || upstream.Response.StatusCode == http.StatusTooManyRequests)) {
			return Identity{}, ErrUnavailable
		}
		return Identity{}, ErrInvalidToken
	}
	idToken, ok := tok.Extra("id_token").(string)
	if !ok || idToken == "" {
		return Identity{}, ErrInvalidToken
	}
	verifier := p.Verifier(&oidc.Config{ClientID: cfg.ClientID, SupportedSigningAlgs: []string{"RS256", "ES256", "PS256"}})
	id, err := verifier.Verify(ctx, idToken)
	if err != nil {
		return Identity{}, ErrInvalidToken
	}
	var verifiedClaims json.RawMessage
	if err := id.Claims(&verifiedClaims); err != nil || authrules.ValidateJSON(verifiedClaims) != nil {
		return Identity{}, ErrInvalidToken
	}
	var claims struct {
		Issuer          string `json:"iss"`
		Subject         string `json:"sub"`
		Nonce           string `json:"nonce"`
		AuthTime        int64  `json:"auth_time"`
		AuthorizedParty string `json:"azp"`
	}
	if err := id.Claims(&claims); err != nil || claims.Subject == "" || claims.Issuer != cfg.Issuer || (req.ExpectedNonce != "" && subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(req.ExpectedNonce)) != 1) {
		return Identity{}, ErrInvalidToken
	}
	if (len(id.Audience) > 1 && claims.AuthorizedParty == "") || (claims.AuthorizedParty != "" && claims.AuthorizedParty != cfg.ClientID) {
		return Identity{}, ErrInvalidToken
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now()
	}
	if id.IssuedAt.IsZero() || id.IssuedAt.After(now.Add(time.Minute)) {
		return Identity{}, ErrInvalidToken
	}
	var authTime time.Time
	if claims.AuthTime > 0 {
		authTime = time.Unix(claims.AuthTime, 0)
	}
	if req.RequireFresh {
		maxAge := req.MaxAge
		if maxAge <= 0 {
			maxAge = 5 * time.Minute
		}
		if claims.AuthTime <= 0 || authTime.After(now.Add(2*time.Second)) || now.Sub(authTime) > maxAge {
			return Identity{}, ErrFreshAuth
		}
	}
	identity := Identity{Protocol: "oidc", Authority: claims.Issuer, Sources: map[string]json.RawMessage{"oidc_id_token": verifiedClaims}, SourceErrors: map[string]string{}, Issuer: claims.Issuer, Subject: claims.Subject, AuthTime: authTime, Nonce: claims.Nonce}
	fillOrdinaryProfile(&identity, verifiedClaims)
	if cfg.UserInfoEnabled {
		endpoint := p.UserInfoEndpoint()
		if endpoint == "" {
			identity.SourceErrors["oidc_userinfo"] = "source_missing"
			return identity, nil
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return Identity{}, ErrInvalidToken
		}
		request.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		request.Header.Set("Accept", "application/json")
		client := c.httpClient()
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := client.Do(request)
		if err != nil {
			if errors.Is(err, ErrInvalidConfig) {
				return Identity{}, err
			}
			identity.SourceErrors["oidc_userinfo"] = "source_unavailable"
			return identity, nil
		}
		defer response.Body.Close()
		if response.StatusCode == 429 || response.StatusCode >= 500 {
			identity.SourceErrors["oidc_userinfo"] = "source_unavailable"
			return identity, nil
		}
		if response.StatusCode == 401 || response.StatusCode == 403 {
			identity.SourceErrors["oidc_userinfo"] = "scope_missing"
			return identity, nil
		}
		media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if response.StatusCode != http.StatusOK || err != nil || media != "application/json" {
			return Identity{}, ErrInvalidToken
		}
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			var networkError net.Error
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &networkError) {
				identity.SourceErrors["oidc_userinfo"] = "source_unavailable"
				return identity, nil
			}
			return Identity{}, ErrInvalidToken
		}
		if authrules.ValidateJSON(raw) != nil {
			return Identity{}, ErrInvalidToken
		}
		var user struct {
			Subject string `json:"sub"`
		}
		if err := json.Unmarshal(raw, &user); err != nil || user.Subject != identity.Subject {
			return Identity{}, ErrInvalidToken
		}
		identity.Sources["oidc_userinfo"] = raw
		fillOrdinaryProfile(&identity, raw)
	}

	return identity, nil
}

// Ordinary profile fields never participate in protocol identity validation.
// Wrong types remain available to the rule engine as unknown attributes.
func fillOrdinaryProfile(identity *Identity, raw json.RawMessage) {
	var profile map[string]json.RawMessage
	if json.Unmarshal(raw, &profile) != nil {
		return
	}
	for field, target := range map[string]*string{"name": &identity.DisplayName, "preferred_username": &identity.PreferredUsername, "email": &identity.Email} {
		if *target == "" {
			var value string
			if json.Unmarshal(profile[field], &value) == nil && len(value) <= 8192 {
				*target = value
			}
		}
	}
}

func normalizedAuthMethod(c Config) string {
	if c.ClientAuthMethod == "post" {
		return "post"
	}
	return "basic"
}
func normalizedScopes(scopes []string) []string {
	if len(scopes) == 0 {
		return []string{"openid", "profile"}
	}
	out := append([]string(nil), scopes...)
	has := false
	for _, s := range out {
		if s == "openid" {
			has = true
		}
	}
	if !has {
		out = append([]string{"openid"}, out...)
	}
	return out
}
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func (c *Client) validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.IsAbs() && u.Host != "" && u.User == nil && u.Fragment == "" && (u.Scheme == "https" || (c.deployment.AllowHTTPLoopback && u.Scheme == "http" && authhttp.IsLoopbackHost(u.Hostname())))
}
func validCodeVerifier(value string) bool {
	if len(value) < 43 || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~", r)) {
			return false
		}
	}
	return true
}
