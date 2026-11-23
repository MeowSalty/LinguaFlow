package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/MeowSalty/LinguaFlow/backend/internal/authrules"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnectionrevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authtransaction"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/externalidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/loginbinding"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/reauthenticationproof"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshsession"
)

func cleanupTestProof(t *testing.T, c *ent.Client, owner *ent.User, now time.Time) int {
	t.Helper()
	return c.ReauthenticationProof.Create().SetTokenHash("expired-cleanup-proof").SetUserID(owner.ID).
		SetAuthVersion(owner.AuthVersion).SetMethod(reauthenticationproof.MethodPassword).
		SetAction(reauthenticationproof.ActionLink).SetTargetID("flow-oidc").
		SetAuthenticatedAt(now.Add(-time.Hour)).SetExpiresAt(now.Add(-time.Second)).SaveX(context.Background()).ID
}

func TestAuthenticationCleanupBoundedCategories(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			now := time.Now().UTC()
			services, clients, _ := externalFlowServices(t, driver, now)
			s, c, ctx := services[0], clients[0], context.Background()
			owner := createTestUser(t, c, "cleanup-owner")
			proofID := cleanupTestProof(t, c, owner, now)
			var sessions []*ent.RefreshSessionCreate
			for i := 0; i < authenticationCleanupBatch+1; i++ {
				for _, revoked := range []bool{false, true} {
					create := c.RefreshSession.Create().SetUserID(owner.ID).SetAuthVersion(owner.AuthVersion).
						SetConnectionID("flow-oidc").SetConnectionGeneration(1).SetLoginPolicyRevision(1).
						SetEvidence(authrules.Evidence{Subject: "retained-secret", ExpiresAt: now.Add(time.Hour)}).
						SetExpiresAt(now.Add(-time.Second))
					if revoked {
						create.SetExpiresAt(now.Add(time.Hour)).SetRevokedAt(now.Add(-time.Second))
					}
					sessions = append(sessions, create)
				}
			}
			c.RefreshSession.CreateBulk(sessions...).SaveX(ctx)
			if err := s.CleanupExpired(ctx); err != nil {
				t.Fatal(err)
			}
			if remaining := c.RefreshSession.Query().Where(refreshsession.EvidenceNotNil()).CountX(ctx); remaining != 2 {
				t.Fatalf("one tick retained %d evidence rows; want independent bounded batches leaving 2", remaining)
			}
			if _, err := c.ReauthenticationProof.Get(ctx, proofID); !ent.IsNotFound(err) {
				t.Fatalf("proof category was starved: %v", err)
			}
			if err := s.CleanupExpired(ctx); err != nil {
				t.Fatal(err)
			}
			if c.RefreshSession.Query().Where(refreshsession.EvidenceNotNil()).CountX(ctx) != 0 {
				t.Fatal("second tick did not finish evidence backlog")
			}
		})
	}
}

func TestAuthenticationCleanupFailureStillCollectsProof(t *testing.T) {
	now := time.Now().UTC()
	services, clients, _ := externalFlowServices(t, dialect.SQLite, now)
	owner := createTestUser(t, clients[0], "cleanup-failure-owner")
	proofID := cleanupTestProof(t, clients[0], owner, now)
	cause := errors.New("evidence unavailable")
	clients[0].RefreshSession.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(context.Context, ent.Query) (ent.Value, error) { return nil, cause })
	}))
	if err := services[0].CleanupExpired(context.Background()); !errors.Is(err, cause) {
		t.Fatalf("lost category error: %v", err)
	}
	if _, err := clients[0].ReauthenticationProof.Get(context.Background(), proofID); !ent.IsNotFound(err) {
		t.Fatalf("failed evidence starved proof: %v", err)
	}
}

func TestAuthenticationCleanupCategoryDeadlineStillCollectsProof(t *testing.T) {
	now := time.Now().UTC()
	services, clients, _ := externalFlowServices(t, dialect.SQLite, now)
	owner := createTestUser(t, clients[0], "cleanup-deadline-owner")
	proofID := cleanupTestProof(t, clients[0], owner, now)
	var queries atomic.Int32
	clients[0].RefreshSession.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
			if queries.Add(1) == 1 {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return next.Query(ctx, query)
		})
	}))
	if err := services[0].CleanupExpired(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked category lost deadline: %v", err)
	}
	if _, err := clients[0].ReauthenticationProof.Get(context.Background(), proofID); !ent.IsNotFound(err) {
		t.Fatalf("blocked category consumed proof budget: %v", err)
	}
}

func TestAuthenticationCleanupBindingKeysetPassesFailedBatch(t *testing.T) {
	now := time.Now().UTC()
	services, clients, _ := externalFlowServices(t, dialect.SQLite, now)
	s, c, ctx := services[0], clients[0], context.Background()
	for i := 0; i < authenticationBindingCleanupBatch+1; i++ {
		owner := createTestUser(t, c, fmt.Sprintf("cleanup-cursor-%d", i))
		identity := c.ExternalIdentity.Create().SetUserID(owner.ID).SetProtocol(externalidentity.ProtocolOidc).
			SetAuthority("https://issuer.example.test").SetSubject(owner.Username).SaveX(ctx)
		c.LoginBinding.Create().SetUserID(owner.ID).SetConnectionID("flow-oidc").SetExternalIdentityID(identity.ID).
			SetEvidence(authrules.Evidence{Subject: owner.Username, ExpiresAt: now.Add(-time.Second)}).SaveX(ctx)
	}
	cause := errors.New("binding batch unavailable")
	var fail atomic.Bool
	fail.Store(true)
	c.LoginBinding.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			if fail.Swap(false) {
				return nil, cause
			}
			return next.Mutate(ctx, mutation)
		})
	})
	if err := s.cleanupBindingEvidence(ctx, now); !errors.Is(err, cause) {
		t.Fatalf("failed batch: %v", err)
	}
	if err := s.cleanupBindingEvidence(ctx, now); err != nil {
		t.Fatal(err)
	}
	if remaining := c.LoginBinding.Query().Where(loginbinding.EvidenceNotNil()).CountX(ctx); remaining != authenticationBindingCleanupBatch {
		t.Fatalf("failed first page blocked keyset progress: remaining=%d", remaining)
	}
	if err := s.cleanupBindingEvidence(ctx, now); err != nil {
		t.Fatal(err)
	}
	if c.LoginBinding.Query().Where(loginbinding.EvidenceNotNil()).CountX(ctx) != 0 {
		t.Fatal("wrapped keyset did not revisit failed batch")
	}
}

func TestAuthenticationCleanupFreshLoginRechecksExpiry(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			now := time.Now().UTC()
			services, clients, _ := externalFlowServices(t, driver, now)
			s, c, ctx := services[0], clients[0], context.Background()
			owner := createTestUser(t, c, "cleanup-fresh-owner")
			identity := c.ExternalIdentity.Create().SetUserID(owner.ID).SetProtocol(externalidentity.ProtocolOidc).SetAuthority("https://issuer.example.test").SetSubject("cleanup-fresh").SaveX(ctx)
			binding := c.LoginBinding.Create().SetUserID(owner.ID).SetConnectionID("flow-oidc").SetExternalIdentityID(identity.ID).
				SetEvidence(authrules.Evidence{Subject: "expired", ExpiresAt: now.Add(-time.Second)}).SaveX(ctx)
			fresh := authrules.Evidence{Subject: "fresh", ExpiresAt: now.Add(time.Hour)}
			var armed atomic.Bool
			armed.Store(true)
			c.LoginBinding.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
				return ent.QuerierFunc(func(ctx context.Context, query ent.Query) (ent.Value, error) {
					value, err := next.Query(ctx, query)
					if err == nil && armed.Swap(false) {
						// The cleanup now holds a stale candidate. Complete fresh login
						// through the same locks before it can enter its write attempt.
						err = withOrganizationTransaction(ctx, clients[1], func(tx *ent.Client) error {
							if err := lockAuthConnection(ctx, tx, binding.ConnectionID); err != nil {
								return err
							}
							if err := lockAuthUser(ctx, tx, owner.ID); err != nil {
								return err
							}
							return tx.LoginBinding.UpdateOneID(binding.ID).SetEvidence(fresh).Select(loginbinding.FieldID).Exec(ctx)
						})
					}
					return value, err
				})
			}))
			if err := s.cleanupBindingEvidence(ctx, now); err != nil {
				t.Fatal(err)
			}
			current := c.LoginBinding.GetX(ctx, binding.ID)
			if current.Evidence.Subject != "fresh" || current.EvidenceExpiresAt == nil || current.EvidenceExpiresAt.Before(fresh.ExpiresAt) {
				t.Fatal("cleanup erased fresh login evidence")
			}
			if err := c.LoginBinding.UpdateOneID(binding.ID).SetEvidence(authrules.Evidence{}).Select(loginbinding.FieldID).Exec(ctx); err != nil {
				t.Fatal(err)
			}
			if c.LoginBinding.GetX(ctx, binding.ID).EvidenceExpiresAt != nil {
				t.Fatal("unrestricted evidence kept an expiry")
			}
			if err := c.LoginBinding.UpdateOneID(binding.ID).SetEvidenceExpiresAt(now).Exec(ctx); err == nil {
				t.Fatal("independent expiry update bypassed evidence invariant")
			}
		})
	}
}

func TestAuthenticationSnapshotCleanupPassesLiveFirstPage(t *testing.T) {
	for _, driver := range []string{dialect.SQLite, dialect.Postgres} {
		t.Run(driver, func(t *testing.T) {
			now := time.Now().UTC()
			services, clients, _ := externalFlowServices(t, driver, now)
			s, c, ctx := services[0], clients[0], context.Background()
			var revisions []*ent.AuthConnectionRevisionCreate
			var flows []*ent.AuthTransactionCreate
			for i := 1; i <= authenticationCleanupBatch; i++ {
				if i > 1 {
					revisions = append(revisions, c.AuthConnectionRevision.Create().SetConnectionID("flow-oidc").SetRevision(int64(i)).SetDisplayName("Live cleanup revision"))
				}
				id := fmt.Sprintf("live-cleanup-%d", i)
				flows = append(flows, c.AuthTransaction.Create().SetID(id).SetPurpose(authtransaction.PurposeLogin).
					SetConnectionID("flow-oidc").SetConnectionRevision(int64(i)).SetConnectionGeneration(1).SetPolicyRevision(1).
					SetStateHash(id).SetNonce("nonce").SetPkceVerifier("verifier").SetBrowserChallenge("challenge").SetCompletionURL("https://app.example.test/auth/complete").SetExpiresAt(now.Add(time.Hour)))
			}
			revisions = append(revisions, c.AuthConnectionRevision.Create().SetConnectionID("flow-oidc").SetRevision(authenticationCleanupBatch+1).SetDisplayName("Stale cleanup revision"))
			c.AuthConnectionRevision.CreateBulk(revisions...).SaveX(ctx)
			c.AuthTransaction.CreateBulk(flows...).SaveX(ctx)
			if err := s.collectConnectionSnapshots(ctx, now); err != nil {
				t.Fatal(err)
			}
			if c.AuthConnectionRevision.Query().Where(authconnectionrevision.ConnectionIDEQ("flow-oidc")).CountX(ctx) != authenticationCleanupBatch {
				t.Fatal("live first page prevented stale snapshot cleanup")
			}
			if c.AuthTransaction.Query().CountX(ctx) != authenticationCleanupBatch {
				t.Fatal("snapshot cleanup collected live flows")
			}
		})
	}
}
