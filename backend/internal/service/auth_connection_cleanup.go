package service

import (
	"context"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authchannelpolicy"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionrevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionsecret"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
)

// collectConnectionSnapshots takes the same connection lock used to start
// flows and save configuration. No secret is collected from a stale refcount.
func (s *ExternalAuthService) collectConnectionSnapshots(ctx context.Context, now time.Time) error {
	ids, err := s.client.AuthConnection.Query().Where(authconnection.IDGT(s.cleanupAfter)).Order(ent.Asc(authconnection.FieldID)).Limit(64).IDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		err = withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
			if err := lockAuthConnection(ctx, c, id); err != nil {
				return err
			}
			conn, err := c.AuthConnection.Get(ctx, id)
			if err != nil {
				return err
			}
			used := map[int64]bool{conn.CurrentRevision: true}
			flows, err := c.AuthTransaction.Query().Where(authtransaction.ConnectionIDEQ(id), authtransaction.ExpiresAtGT(now)).All(ctx)
			if err != nil {
				return err
			}
			for _, flow := range flows {
				used[flow.ConnectionRevision] = true
			}
			revisions, err := c.AuthConnectionRevision.Query().Where(authconnectionrevision.ConnectionIDEQ(id)).Order(ent.Asc(authconnectionrevision.FieldRevision)).Limit(256).All(ctx)
			if err != nil {
				return err
			}
			for _, revision := range revisions {
				if used[revision.Revision] {
					continue
				}
				if err := c.AuthConnectionRevision.DeleteOneID(revision.ID).Exec(ctx); err != nil {
					return err
				}
				if _, err := c.AuthChannelPolicy.Delete().Where(authchannelpolicy.ConnectionIDEQ(id), authchannelpolicy.RevisionEQ(revision.Revision)).Exec(ctx); err != nil {
					return err
				}
			}
			stored, err := c.AuthConnectionSecret.Query().Where(authconnectionsecret.ConnectionIDEQ(id)).Order(ent.Asc(authconnectionsecret.FieldVersion)).Limit(256).All(ctx)
			if err != nil {
				return err
			}
			for _, secret := range stored {
				used, err := c.AuthConnectionRevision.Query().Where(authconnectionrevision.ConnectionIDEQ(id), authconnectionrevision.SecretVersionEQ(secret.Version)).Exist(ctx)
				if err != nil {
					return err
				}
				if !used {
					if err := c.AuthConnectionSecret.DeleteOneID(secret.ID).Exec(ctx); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		s.cleanupAfter = id
	}
	if len(ids) < 64 {
		s.cleanupAfter = ""
	}
	return nil
}
