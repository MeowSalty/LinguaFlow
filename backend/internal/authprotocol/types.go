package authprotocol

import (
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalidConfig = errors.New("invalid external authentication configuration")
	ErrUnavailable   = errors.New("identity provider unavailable")
	ErrInvalidToken  = errors.New("invalid identity provider response")
	ErrFreshAuth     = errors.New("recent authentication required")
)

type Identity struct {
	Protocol          string
	Authority         string
	Sources           map[string]json.RawMessage
	SourceErrors      map[string]string
	Issuer            string
	Subject           string
	AuthTime          time.Time
	DisplayName       string
	PreferredUsername string
	Email             string
	Nonce             string
}

type ProviderMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	UserInfoEndpoint      string   `json:"userinfo_endpoint,omitempty"`
	ScopesSupported       []string `json:"scopes_supported,omitempty"`
	AuthStyle             string   `json:"client_auth_method"`
	SupportsPKCES256      bool     `json:"supports_pkce_s256"`
}

type AuthorizationRequest struct {
	RedirectURL  string
	State        string
	Nonce        string
	CodeVerifier string
	Fresh        bool
	MaxAge       time.Duration
}

type Authorization struct {
	URL           string
	CodeChallenge string
}

type ExchangeRequest struct {
	RedirectURL   string
	Code          string
	CodeVerifier  string
	ExpectedNonce string
	RequireFresh  bool
	MaxAge        time.Duration
	Now           time.Time
}
