package service

import (
	"context"
	"time"

	"entgo.io/ent/dialect/sql"
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
		// A blocked connection must not become the first candidate forever.
		// The next tick advances; reaching the end wraps and revisits failures.
		s.cleanupAfter = id
		err = withOrganizationTransaction(ctx, s.client, func(c *ent.Client) error {
			if err := lockAuthConnection(ctx, c, id); err != nil {
				return err
			}
			conn, err := c.AuthConnection.Query().Where(authconnection.IDEQ(id)).Select(authconnection.FieldID, authconnection.FieldCurrentRevision).Only(ctx)
			if err != nil {
				return err
			}
			revisions, err := c.AuthConnectionRevision.Query().Where(authconnectionrevision.ConnectionIDEQ(id), authconnectionrevision.RevisionNEQ(conn.CurrentRevision), func(selector *sql.Selector) {
				flow := sql.Table(authtransaction.Table).As("live_flow")
				live := sql.Dialect(selector.Dialect()).Select(flow.C(authtransaction.FieldID)).From(flow).Where(sql.And(
					sql.ColumnsEQ(flow.C(authtransaction.FieldConnectionID), selector.C(authconnectionrevision.FieldConnectionID)),
					sql.ColumnsEQ(flow.C(authtransaction.FieldConnectionRevision), selector.C(authconnectionrevision.FieldRevision)),
					sql.GT(flow.C(authtransaction.FieldExpiresAt), now),
				))
				selector.Where(sql.Not(sql.Exists(live)))
			}).Order(ent.Asc(authconnectionrevision.FieldRevision)).Select(authconnectionrevision.FieldID, authconnectionrevision.FieldRevision).Limit(authenticationCleanupBatch).All(ctx)
			if err != nil {
				return err
			}
			if len(revisions) > 0 {
				revisionIDs, versions := make([]int, 0, len(revisions)), make([]int64, 0, len(revisions))
				for _, revision := range revisions {
					revisionIDs = append(revisionIDs, revision.ID)
					versions = append(versions, revision.Revision)
				}
				if _, err := c.AuthConnectionRevision.Delete().Where(authconnectionrevision.IDIn(revisionIDs...)).Exec(ctx); err != nil {
					return err
				}
				if _, err := c.AuthChannelPolicy.Delete().Where(authchannelpolicy.ConnectionIDEQ(id), authchannelpolicy.RevisionIn(versions...)).Exec(ctx); err != nil {
					return err
				}
			}
			stored, err := c.AuthConnectionSecret.Query().Where(authconnectionsecret.ConnectionIDEQ(id), func(selector *sql.Selector) {
				revision := sql.Table(authconnectionrevision.Table).As("using_revision")
				used := sql.Dialect(selector.Dialect()).Select(revision.C(authconnectionrevision.FieldID)).From(revision).Where(sql.And(
					sql.ColumnsEQ(revision.C(authconnectionrevision.FieldConnectionID), selector.C(authconnectionsecret.FieldConnectionID)),
					sql.ColumnsEQ(revision.C(authconnectionrevision.FieldSecretVersion), selector.C(authconnectionsecret.FieldVersion)),
				))
				selector.Where(sql.Not(sql.Exists(used)))
			}).Order(ent.Asc(authconnectionsecret.FieldVersion)).Limit(authenticationCleanupBatch).IDs(ctx)
			if err != nil {
				return err
			}
			if len(stored) > 0 {
				if _, err := c.AuthConnectionSecret.Delete().Where(authconnectionsecret.IDIn(stored...)).Exec(ctx); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(ids) < 64 {
		s.cleanupAfter = ""
	}
	return nil
}
