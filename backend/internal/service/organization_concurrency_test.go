package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/orgmembership"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

func TestOrganizationSQLiteIndependentTransactionCompetition(t *testing.T) {
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "organizations.db")) + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(0)"
	newClient := func() *ent.Client {
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(2)
		client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB(dialect.SQLite, db))))
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
	first, second := newClient(), newClient()
	if err := first.Schema.Create(context.Background()); err != nil {
		t.Fatal(err)
	}
	runOrganizationTransactionCompetition(t, first, second)
}

func TestOrganizationPostgresIndependentTransactionCompetition(t *testing.T) {
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cfg := config.DefaultServerConfig()
	cfg.Database = config.DatabaseConfig{Driver: config.DatabaseDriverPostgres, DSN: dsn, MaxOpenConns: 2, MaxIdleConns: 2}
	db, first, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	_, second, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	unlock, err := database.AcquireMigrationLock(ctx, db, config.DatabaseDriverPostgres)
	if err != nil {
		t.Fatal(err)
	}
	err = first.Schema.Create(ctx)
	unlockErr := unlock()
	if err != nil {
		t.Fatal(err)
	}
	if unlockErr != nil {
		t.Fatal(unlockErr)
	}
	runOrganizationTransactionCompetition(t, first, second)
}

func runOrganizationTransactionCompetition(t *testing.T, first, second *ent.Client) {
	t.Helper()
	for _, scenario := range []string{"self_demotion", "mutual_removal", "self_leave", "demotion_against_grant", "duplicate_member"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			left, right := first, second
			// Gates check each case's IDs; prior hooks are already released.
			suffix := fmt.Sprintf("%d", time.Now().UnixNano())
			a := organizationTestUser(t, first, "owner-a", suffix)
			b := organizationTestUser(t, first, "owner-b", suffix)
			target := organizationTestUser(t, first, "target", suffix)
			svcA, svcB := NewUserService(left, nil), NewUserService(right, nil)
			org, err := svcA.CreateOrganization(ctx, a.ID, CreateOrganizationInput{Name: "Concurrency " + suffix, Slug: "concurrency-" + suffix})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svcA.AddMember(ctx, a.ID, org.ID, AddOrgMemberInput{Username: b.Username, Role: organizationRole(OrgRoleOwner)}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanup := context.Background()
				_, _ = first.ActivityLog.Delete().Where(activitylog.HasOrganizationWith(organization.IDEQ(org.ID))).Exec(cleanup)
				_, _ = first.OrgMembership.Delete().Where(orgmembership.HasOrganizationWith(organization.IDEQ(org.ID))).Exec(cleanup)
				_ = first.Organization.DeleteOneID(org.ID).Exec(cleanup)
				_, _ = first.User.Delete().Where(user.IDIn(a.ID, b.ID, target.ID)).Exec(cleanup)
			})
			entered, release, attempted := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var enterOnce, releaseOnce, attemptOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			// Hook only the first operation: it waits after the organization lock
			// and owner count, while another independent transaction tries to write.
			var gatedMembershipID int
			if scenario != "duplicate_member" {
				gatedUserID := a.ID
				if scenario == "mutual_removal" || scenario == "demotion_against_grant" {
					gatedUserID = b.ID
				}
				gatedMembershipID = first.OrgMembership.Query().Where(orgmembership.HasOrganizationWith(organization.IDEQ(org.ID)), orgmembership.HasUserWith(user.IDEQ(gatedUserID))).OnlyX(ctx).ID
			}
			left.OrgMembership.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					m := mutation.(*ent.OrgMembershipMutation)
					id, _ := m.ID()
					organizationID, _ := m.OrganizationID()
					if id == gatedMembershipID && gatedMembershipID != 0 || scenario == "duplicate_member" && organizationID == org.ID {
						enterOnce.Do(func() { close(entered) })
						select {
						case <-release:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
					return next.Mutate(ctx, mutation)
				})
			})
			right.Organization.Use(func(next ent.Mutator) ent.Mutator {
				return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
					if mutation.Op() == ent.OpUpdate {
						attemptOnce.Do(func() { close(attempted) })
					}
					return next.Mutate(ctx, mutation)
				})
			})
			operationA := func() error {
				switch scenario {
				case "self_demotion":
					_, err := svcA.UpdateMemberRole(ctx, a.ID, org.ID, a.ID, UpdateOrgMemberRoleInput{Role: OrgRoleMember})
					return err
				case "mutual_removal":
					return svcA.RemoveMember(ctx, a.ID, org.ID, b.ID)
				case "self_leave":
					return svcA.RemoveMember(ctx, a.ID, org.ID, a.ID)
				case "demotion_against_grant":
					_, err := svcA.UpdateMemberRole(ctx, a.ID, org.ID, b.ID, UpdateOrgMemberRoleInput{Role: OrgRoleMember})
					return err
				default:
					_, err := svcA.AddMember(ctx, a.ID, org.ID, AddOrgMemberInput{Username: target.Username})
					return err
				}
			}
			operationB := func() error {
				switch scenario {
				case "self_demotion":
					_, err := svcB.UpdateMemberRole(ctx, b.ID, org.ID, b.ID, UpdateOrgMemberRoleInput{Role: OrgRoleMember})
					return err
				case "mutual_removal":
					return svcB.RemoveMember(ctx, b.ID, org.ID, a.ID)
				case "self_leave":
					return svcB.RemoveMember(ctx, b.ID, org.ID, b.ID)
				case "demotion_against_grant":
					_, err := svcB.AddMember(ctx, b.ID, org.ID, AddOrgMemberInput{Username: target.Username, Role: organizationRole(OrgRoleOwner)})
					return err
				default:
					_, err := svcB.AddMember(ctx, b.ID, org.ID, AddOrgMemberInput{Username: target.Username})
					return err
				}
			}
			resultA, resultB := make(chan error, 1), make(chan error, 1)
			go func() { resultA <- operationA() }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("first transaction did not reach gate")
			}
			go func() { resultB <- operationB() }()
			select {
			case <-attempted:
			case <-ctx.Done():
				t.Fatal("independent transaction did not attempt organization lock")
			}
			select {
			case err := <-resultB:
				t.Fatalf("second mutation completed before first released its lock: %v", err)
			case <-time.After(25 * time.Millisecond):
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case err := <-resultA:
				if err != nil {
					t.Fatalf("first mutation: %v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			want := ErrOwnerRequired
			if scenario == "mutual_removal" || scenario == "demotion_against_grant" {
				want = ErrForbidden
			}
			if scenario == "duplicate_member" {
				want = ErrMembershipExists
			}
			select {
			case err := <-resultB:
				if !errors.Is(err, want) {
					t.Fatalf("second mutation=%v want=%v", err, want)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			owners, err := organizationOwnerCount(ctx, first, org.ID)
			if err != nil || owners < 1 {
				t.Fatalf("owner invariant: count=%d err=%v", owners, err)
			}
			if scenario == "demotion_against_grant" && first.OrgMembership.Query().Where(orgmembership.HasOrganizationWith(organization.IDEQ(org.ID)), orgmembership.HasUserWith(user.IDEQ(target.ID))).ExistX(ctx) {
				t.Fatal("grant used stale authorization")
			}
		})
	}
}

func TestOrganizationTransactionRetriesAreBounded(t *testing.T) {
	ctx := context.Background()
	f := newOrganizationFixture(t)
	for _, code := range []string{"40001", "40P01", "55P03"} {
		attempts := 0
		err := withOrganizationMutation(ctx, f.client, f.org.ID, func(*ent.Client) error { attempts++; return &pgconn.PgError{Code: code} })
		if err == nil || attempts != organizationMutationAttempts {
			t.Fatalf("code=%s attempts=%d err=%v", code, attempts, err)
		}
	}
	for _, cause := range []error{io.EOF, &pgconn.PgError{Code: "08007"}, ErrForbidden, ErrOwnerRequired} {
		attempts := 0
		err := withOrganizationMutation(ctx, f.client, f.org.ID, func(*ent.Client) error { attempts++; return cause })
		if !errors.Is(err, cause) || attempts != 1 {
			t.Fatalf("unsafe retry: attempts=%d err=%v", attempts, err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	forbidden := false
	err := withOrganizationMutation(cancelled, f.client, f.org.ID, func(*ent.Client) error { cancel(); forbidden = true; return &pgconn.PgError{Code: "40001"} })
	if !forbidden || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled retry=%v", err)
	}
}

func TestOrganizationCancelledRollbackKeepsUncertainty(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.client.Organization.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			tx, err := mutation.(*ent.OrganizationMutation).Tx()
			if err != nil {
				return nil, err
			}
			tx.OnRollback(func(next ent.Rollbacker) ent.Rollbacker {
				return ent.RollbackFunc(func(ctx context.Context, tx *ent.Tx) error {
					// Force the cancellation race's ambiguous acknowledgement even
					// when this test wins the race against automatic rollback.
					return errors.Join(next.Rollback(ctx, tx), sql.ErrTxDone)
				})
			})
			return next.Mutate(ctx, mutation)
		})
	})
	attempts := 0
	err := withOrganizationMutation(ctx, f.client, f.org.ID, func(*ent.Client) error {
		attempts++
		cancel()
		return &pgconn.PgError{Code: "40001"}
	})
	var uncertain *organizationRollbackError
	if !errors.Is(err, context.Canceled) || !errors.As(err, &uncertain) || !errors.Is(uncertain.rollback, sql.ErrTxDone) {
		t.Fatalf("cancellation lost rollback uncertainty: %v", err)
	}
	if attempts != 1 || isOrganizationTransactionConflict(err) {
		t.Fatalf("uncertain cancelled operation became retryable: attempts=%d err=%v", attempts, err)
	}
}
