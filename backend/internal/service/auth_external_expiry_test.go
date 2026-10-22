package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
)

func TestExternalAuthDeadlinesAndFailureTickets(t *testing.T) {
	for _, scenario := range []string{"transaction", "exchange", "confirmation", "upstream finishes late", "denied", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC()
			svcs, clients, protocol := externalFlowServices(t, dialect.SQLite, now)
			ctx := context.Background()
			start := flowTestStart(t, svcs[0], "flow-oidc")
			if _, err := svcs[0].HandleCallback(ctx, "flow-oidc", "unknown-state", "authorization-code-secret", ""); !errors.Is(err, ErrExternalFlowInvalid) {
				t.Fatalf("unknown state: %v", err)
			}
			if scenario == "transaction" {
				svcs[0].now = func() time.Time { return start.ExpiresAt }
			}
			if scenario == "upstream finishes late" {
				protocol.exchange = func(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error) {
					svcs[0].now = func() time.Time { return start.ExpiresAt }
					return authoidc.Identity{Issuer: "https://issuer.example.test", Subject: "external-subject"}, nil
				}
			}
			if scenario == "unavailable" {
				protocol.exchange = func(context.Context, authoidc.Config, authoidc.ExchangeRequest) (authoidc.Identity, error) {
					return authoidc.Identity{}, authoidc.ErrUnavailable
				}
			}
			providerError := ""
			if scenario == "denied" {
				providerError = "access_denied"
			}
			cb, err := svcs[0].HandleCallback(ctx, "flow-oidc", protocol.state, "authorization-code-secret", providerError)
			if scenario == "transaction" || scenario == "upstream finishes late" {
				if !errors.Is(err, ErrExternalFlowInvalid) || cb.Ticket != "" || clients[0].AuthTransaction.GetX(ctx, start.TransactionID).Status != authtransaction.StatusExpired {
					t.Fatalf("expired callback redirected: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svcs[0].HandleCallback(ctx, "flow-oidc", protocol.state, "authorization-code-secret", ""); !errors.Is(err, ErrExternalFlowInvalid) {
				t.Fatalf("state replay: %v", err)
			}
			if scenario == "exchange" {
				svcs[0].now = func() time.Time { return now.Add(externalTicketTTL) }
			}
			x, err := svcs[0].Exchange(ctx, nil, ExternalExchangeInput{Ticket: cb.Ticket, Verifier: flowTestVerifier})
			if scenario == "confirmation" {
				if err != nil {
					t.Fatal(err)
				}
				svcs[0].now = func() time.Time { return x.Confirmation.ExpiresAt }
				_, err = svcs[0].Complete(ctx, nil, ExternalCompleteInput{ConfirmationToken: x.Confirmation.ConfirmationToken, Verifier: flowTestVerifier, Username: "expired-owner"})
			}
			want := ErrExternalFlowInvalid
			if scenario == "unavailable" {
				want = ErrExternalUnavailable
			}
			if !errors.Is(err, want) {
				t.Fatalf("deadline/failure classification: %v", err)
			}
			if _, err := svcs[0].Exchange(ctx, nil, ExternalExchangeInput{Ticket: cb.Ticket, Verifier: flowTestVerifier}); !errors.Is(err, ErrExternalFlowInvalid) {
				t.Fatalf("failure ticket reused: %v", err)
			}
			if clients[0].User.Query().CountX(ctx) != 0 || clients[0].LoginBinding.Query().CountX(ctx) != 0 {
				t.Fatal("failed flow created account state")
			}
		})
	}
}

func TestExternalAuthReturnToRejectsEncodedExternalPaths(t *testing.T) {
	for _, raw := range []string{"//evil.example", "/%2fevil.example", "/%5cevil.example", "/%0d%0aLocation:evil", "https://evil.example"} {
		if validReturnTo(raw) {
			t.Fatalf("unsafe return path accepted: %q", raw)
		}
	}
	if !validReturnTo("/settings?section=login") {
		t.Fatal("valid relative path rejected")
	}
}
