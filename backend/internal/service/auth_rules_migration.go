package service

import (
	"context"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

// This is a one-time storage conversion, never a runtime policy fallback.
func migrateAuthenticationRules(ctx context.Context, c *ent.Client) error {
	policies, err := c.AuthChannelPolicy.Query().All(ctx)
	if err != nil {
		return err
	}
	for _, p := range policies {
		if p.LoginRules.Mode != "" && p.SignupRules.Mode != "" && p.LinkRules.Mode != "" {
			continue
		}
		if p.LoginRules.Mode != "" || p.SignupRules.Mode != "" || p.LinkRules.Mode != "" || p.LegacyLoginRule != "unrestricted" || p.LegacySignupRule != "unrestricted" || p.LegacyLinkRule != "unrestricted" {
			return ErrSettingsUnavailable
		}
		// Snapshot immutability applies to application writes, not schema migration.
		if _, err = c.ExecContext(ctx, fmt.Sprintf(`UPDATE auth_channel_policies SET login_rules='{"mode":"unrestricted"}', signup_rules='{"mode":"unrestricted"}', link_rules='{"mode":"unrestricted"}' WHERE id=%d`, p.ID)); err != nil {
			return err
		}
	}
	now := timeutil.NowUTC()
	if _, err = c.AuthTransaction.Update().Where(authtransaction.StatusIn(authtransaction.StatusCreated, authtransaction.StatusCallbackProcessing, authtransaction.StatusCallbackVerified, authtransaction.StatusAwaitingConfirmation)).SetStatus(authtransaction.StatusCancelled).SetFinishedAt(now).ClearExchangeTicketHash().ClearConfirmationTokenHash().ClearEvidence().Save(ctx); err != nil {
		return err
	}
	_, err = c.ReauthenticationProof.Delete().Exec(ctx)
	return err
}
