package service

import (
	"context"
	crand "crypto/rand"
	"errors"
	"io"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

const (
	externalTransactionTTL  = 10 * time.Minute
	externalTicketTTL       = time.Minute
	externalConfirmationTTL = 5 * time.Minute
	reauthenticationTTL     = 5 * time.Minute
)

var (
	ErrExternalFlowInvalid      = errors.New("external authentication flow is invalid or consumed")
	ErrExternalUnavailable      = errors.New("external identity provider is unavailable")
	ErrReauthenticationRequired = errors.New("reauthentication proof is missing, invalid, expired, or consumed")
	ErrLoginMethodConflict      = errors.New("account must retain a login and maintenance method")
)

type OIDCProtocol interface {
	Check(context.Context, authoidc.Config) (authoidc.ProviderMetadata, error)
	BuildAuthorization(context.Context, authoidc.Config, authoidc.AuthorizationRequest) (authoidc.Authorization, error)
	ExchangeAndVerify(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error)
}

type ExternalAuthService struct {
	client   *ent.Client
	auth     *AuthService
	keys     *credential.Keyring
	cfg      config.AuthDeploymentConfig
	protocol OIDCProtocol
	now      func() time.Time
	rand     io.Reader
}

type externalActorContextKey struct{}
type externalActorVersion struct {
	userID      int
	authVersion int64
}

// WithExternalActor binds a service call to the exact bearer generation that
// authorized it. Sensitive writes recheck this after acquiring the user lock.
func WithExternalActor(ctx context.Context, userID int, authVersion int64) context.Context {
	return context.WithValue(ctx, externalActorContextKey{}, externalActorVersion{userID: userID, authVersion: authVersion})
}
func checkExternalActor(ctx context.Context, account *ent.User) error {
	v, ok := ctx.Value(externalActorContextKey{}).(externalActorVersion)
	if !ok {
		return nil
	}
	if account == nil || v.userID != account.ID || v.authVersion != account.AuthVersion {
		return ErrTokenInvalid
	}
	return nil
}

func NewExternalAuthService(client *ent.Client, auth *AuthService, keys *credential.Keyring, cfg config.AuthDeploymentConfig) (*ExternalAuthService, error) {
	protocol, err := authoidc.New(cfg)
	if err != nil {
		return nil, err
	}
	return &ExternalAuthService{client: client, auth: auth, keys: keys, cfg: cfg, protocol: protocol, now: timeutil.NowUTC, rand: crand.Reader}, nil
}

func (s *ExternalAuthService) SetOIDCProtocol(protocol OIDCProtocol) { s.protocol = protocol }

type ExternalBrowserInput struct{ BrowserChallenge, CompletionURL, ReturnTo string }
type ExternalLinkStartInput struct {
	ExternalBrowserInput
	ReauthenticationProof string
}
type ReauthenticationInput struct {
	Method, Password, Action, TargetID        string
	BindingID                                 *int
	BrowserChallenge, CompletionURL, ReturnTo string
}
type ExternalStartResult struct {
	TransactionID, AuthorizationURL string
	ExpiresAt                       time.Time
}
type ExternalCallbackResult struct {
	CompletionURL, Ticket, TransactionID, ReturnTo string
	Failed                                         bool
}
type ExternalExchangeInput struct{ Ticket, Verifier string }
type ExternalCompleteInput struct{ ConfirmationToken, Verifier, Username, DisplayName string }
type ExternalCancelInput struct{ TransactionID, Verifier string }
type ExternalProfile struct{ ConnectionID, ConnectionName, ExternalDisplay string }
type ExternalConfirmation struct {
	TransactionID, ConfirmationToken string
	ExpiresAt                        time.Time
	Profile                          ExternalProfile
}
type ReauthenticationResult struct {
	Proof     string
	ExpiresAt time.Time
}
type ExternalTestResult struct {
	Attributes     map[string]authrules.Attribute
	Diagnostics    AuthRulesDiagnostics `json:"rules"`
	ConnectionID   string
	Revision       int64
	FreshSupported bool
}
type ExternalExchangeResult struct {
	Kind         string
	Session      *Session
	Confirmation *ExternalConfirmation
	Proof        *ReauthenticationResult
	Test         *ExternalTestResult
}
type ExternalCompleteResult struct {
	Kind      string
	Session   *Session
	BindingID int
}
