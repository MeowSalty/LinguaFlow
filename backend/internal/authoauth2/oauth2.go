// Package authoauth2 implements the deliberately bounded confidential-client
// authorization-code protocol. It never interprets an OAuth token as an ID token.
package authoauth2

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authhttp"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authprotocol"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
)

type SubjectMapping struct {
	Pointer string `json:"pointer"`
	Type    string `json:"type"`
}
type Config struct {
	Authority             string
	AuthorizationEndpoint string
	TokenEndpoint         string
	ProfileEndpoint       string
	ClientID              string
	ClientSecret          string
	ClientAuthMethod      string
	Scopes                []string
	SubjectMapping        SubjectMapping
}
type Client struct {
	transport *authhttp.Transport
	timeout   time.Duration
}

func New(d config.AuthDeploymentConfig) (*Client, error) {
	if d.RequestTimeout == 0 {
		d.RequestTimeout = 10 * time.Second
	}
	if d.MaxResponseBytes == 0 {
		d.MaxResponseBytes = 1 << 20
	}
	if d.RequestTimeout < 0 || d.MaxResponseBytes < 0 {
		return nil, authprotocol.ErrInvalidConfig
	}
	return &Client{transport: authhttp.New(d), timeout: d.RequestTimeout}, nil
}

func (c *Client) Check(_ context.Context, cfg Config) (authprotocol.ProviderMetadata, error) {
	if cfg.ClientID == "" || len(cfg.ClientID) > 1024 || cfg.Authority == "" || cfg.SubjectMapping.Type != "string" && cfg.SubjectMapping.Type != "integer" || authrules.ValidatePointer(cfg.SubjectMapping.Pointer) != nil {
		return authprotocol.ProviderMetadata{}, authprotocol.ErrInvalidConfig
	}
	if cfg.ClientAuthMethod != "client_secret_basic" && cfg.ClientAuthMethod != "client_secret_post" {
		return authprotocol.ProviderMetadata{}, authprotocol.ErrInvalidConfig
	}
	for _, endpoint := range []string{cfg.AuthorizationEndpoint, cfg.TokenEndpoint, cfg.ProfileEndpoint} {
		if len(endpoint) > 2048 || !c.transport.ValidRawURL(endpoint) {
			return authprotocol.ProviderMetadata{}, authprotocol.ErrInvalidConfig
		}
		u, _ := url.Parse(endpoint)
		// Protocol parameters are server-owned, including ones embedded in URLs.
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return authprotocol.ProviderMetadata{}, authprotocol.ErrInvalidConfig
		}
		for _, name := range []string{"response_type", "client_id", "client_secret", "redirect_uri", "scope", "state", "code", "code_verifier", "code_challenge", "code_challenge_method", "grant_type", "access_token"} {
			if q.Has(name) {
				return authprotocol.ProviderMetadata{}, authprotocol.ErrInvalidConfig
			}
		}
	}
	seen := map[string]bool{}
	if len(cfg.Scopes) > 32 {
		return authprotocol.ProviderMetadata{}, authprotocol.ErrInvalidConfig
	}
	for _, scope := range cfg.Scopes {
		if scope == "" || len(scope) > 128 || strings.ContainsAny(scope, " \t\r\n") || seen[scope] {
			return authprotocol.ProviderMetadata{}, authprotocol.ErrInvalidConfig
		}
		seen[scope] = true
	}
	return authprotocol.ProviderMetadata{AuthorizationEndpoint: cfg.AuthorizationEndpoint, TokenEndpoint: cfg.TokenEndpoint, UserInfoEndpoint: cfg.ProfileEndpoint, AuthStyle: cfg.ClientAuthMethod, SupportsPKCES256: true}, nil
}

var verifierPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
var integerPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)

func (c *Client) BuildAuthorization(ctx context.Context, cfg Config, r authprotocol.AuthorizationRequest) (authprotocol.Authorization, error) {
	if _, err := c.Check(ctx, cfg); err != nil {
		return authprotocol.Authorization{}, err
	}
	if r.State == "" || !verifierPattern.MatchString(r.CodeVerifier) || !c.transport.ValidRawURL(r.RedirectURL) || r.Fresh {
		return authprotocol.Authorization{}, authprotocol.ErrInvalidConfig
	}
	u, _ := url.Parse(cfg.AuthorizationEndpoint)
	q := u.Query()
	sum := sha256.Sum256([]byte(r.CodeVerifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", r.RedirectURL)
	q.Set("state", r.State)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	if len(cfg.Scopes) > 0 {
		q.Set("scope", strings.Join(cfg.Scopes, " "))
	}
	u.RawQuery = q.Encode()
	return authprotocol.Authorization{URL: u.String(), CodeChallenge: challenge}, nil
}

func (c *Client) jsonRequest(req *http.Request) (map[string]json.RawMessage, []byte, error) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "LinguaFlow-authentication")
	hc := &http.Client{Transport: c.transport, Timeout: c.timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := hc.Do(req)
	if err != nil {
		return nil, nil, authprotocol.ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode == 429 || res.StatusCode >= 500 {
		return nil, nil, authprotocol.ErrUnavailable
	}
	media, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, nil, authprotocol.ErrInvalidToken
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, nil, authprotocol.ErrUnavailable
	}
	if authrules.ValidateJSON(raw) != nil {
		return nil, nil, authprotocol.ErrInvalidToken
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, nil, authprotocol.ErrInvalidToken
	}
	return obj, raw, nil
}

func (c *Client) ExchangeAndVerify(ctx context.Context, cfg Config, r authprotocol.ExchangeRequest) (authprotocol.Identity, error) {
	if _, err := c.Check(ctx, cfg); err != nil {
		return authprotocol.Identity{}, err
	}
	if cfg.ClientSecret == "" || r.Code == "" || !verifierPattern.MatchString(r.CodeVerifier) || !c.transport.ValidRawURL(r.RedirectURL) {
		return authprotocol.Identity{}, authprotocol.ErrInvalidConfig
	}
	if r.RequireFresh {
		return authprotocol.Identity{}, authprotocol.ErrFreshAuth
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {r.Code}, "redirect_uri": {r.RedirectURL}, "code_verifier": {r.CodeVerifier}}
	if cfg.ClientAuthMethod == "client_secret_post" {
		form.Set("client_id", cfg.ClientID)
		form.Set("client_secret", cfg.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return authprotocol.Identity{}, authprotocol.ErrInvalidConfig
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cfg.ClientAuthMethod == "client_secret_basic" {
		req.SetBasicAuth(url.QueryEscape(cfg.ClientID), url.QueryEscape(cfg.ClientSecret))
	}
	obj, _, err := c.jsonRequest(req)
	if err != nil {
		return authprotocol.Identity{}, err
	}
	if _, hasError := obj["error"]; hasError {
		return authprotocol.Identity{}, authprotocol.ErrInvalidToken
	}
	var token, tokenType string
	if json.Unmarshal(obj["access_token"], &token) != nil || token == "" || strings.ContainsAny(token, "\r\n") || json.Unmarshal(obj["token_type"], &tokenType) != nil || !strings.EqualFold(tokenType, "Bearer") {
		return authprotocol.Identity{}, authprotocol.ErrInvalidToken
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, cfg.ProfileEndpoint, nil)
	if err != nil {
		return authprotocol.Identity{}, authprotocol.ErrInvalidConfig
	}
	req.Header.Set("Authorization", "Bearer "+token)
	_, raw, err := c.jsonRequest(req)
	if err != nil {
		return authprotocol.Identity{}, err
	}
	v, ok := authrules.JSONValue(raw, cfg.SubjectMapping.Pointer)
	if !ok {
		return authprotocol.Identity{}, authprotocol.ErrInvalidToken
	}
	var subject string
	if cfg.SubjectMapping.Type == "string" {
		subject, ok = v.(string)
		if !ok || subject == "" || len(subject) > 8192 {
			return authprotocol.Identity{}, authprotocol.ErrInvalidToken
		}
	} else {
		n, valid := v.(json.Number)
		if !valid || !integerPattern.MatchString(string(n)) {
			return authprotocol.Identity{}, authprotocol.ErrInvalidToken
		}
		num, valid := new(big.Int).SetString(string(n), 10)
		if !valid {
			return authprotocol.Identity{}, authprotocol.ErrInvalidToken
		}
		subject = num.String()
	}
	return authprotocol.Identity{Protocol: "oauth2", Authority: cfg.Authority, Subject: subject, Sources: map[string]json.RawMessage{"oauth2_profile": raw}, SourceErrors: map[string]string{}}, nil
}
