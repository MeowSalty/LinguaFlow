package service

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// Version 4 only admitted OIDC connections. Existing identities already have
// protocol/authority/subject; no identity, account or session is reassigned.
func migrateAuthenticationOAuth(ctx context.Context, c *ent.Client) error {
	if _, err := c.ExecContext(ctx, "UPDATE auth_transactions SET identity_protocol='oidc' WHERE connection_id IN (SELECT id FROM auth_connections WHERE kind='oidc')"); err != nil {
		return err
	}
	now := timeutil.NowUTC()
	if _, err := c.AuthTransaction.Update().Where(authtransaction.StatusIn(authtransaction.StatusCreated, authtransaction.StatusCallbackProcessing, authtransaction.StatusCallbackVerified, authtransaction.StatusAwaitingConfirmation)).SetStatus(authtransaction.StatusCancelled).SetFinishedAt(now).ClearExchangeTicketHash().ClearExchangeTicketExpiresAt().ClearConfirmationTokenHash().ClearConfirmationExpiresAt().ClearEvidence().Save(ctx); err != nil {
		return err
	}
	_, err := c.ReauthenticationProof.Delete().Exec(ctx)
	return err
}
