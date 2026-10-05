package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagecheckwrite"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
)

func TestStorageCheckInterruptedRecoveryKeepsAuthorizationFact(t *testing.T) {
	ctx, client, s, u, c, _, _ := storageConnectionFixture(t)
	for _, activated := range []bool{false, true} {
		row := client.StorageCheck.Create().SetConnectionID(c.ID).SetActorID(u.ID).SetMode("write").SetManagementGeneration(c.ManagementGeneration).SetAuthorizationActivated(activated).SetDeadline(time.Now().Add(-time.Minute)).SaveX(ctx)
		if err := s.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		got := client.StorageCheck.GetX(ctx, row.ID)
		if activated {
			if got.Status != "completed" || !got.AuthorizationActivated {
				t.Fatal("lost committed authorization fact")
			}
		} else if got.Status != "failed" || got.ErrorCode != "storage_timeout" || got.AuthorizationActivated {
			t.Fatal("interrupted check silently activated authorization")
		}
	}
	if client.StorageConnection.GetX(ctx, c.ID).ActiveAuthGeneration != 0 {
		t.Fatal("check recovery changed current connection")
	}
	live := client.StorageCheck.Create().SetConnectionID(c.ID).SetActorID(u.ID).SetMode("read_only").SetManagementGeneration(c.ManagementGeneration).SetCreatedAt(time.Now().Add(-time.Hour)).SetDeadline(time.Now().Add(time.Hour)).SaveX(ctx)
	s.cfg.TransferTimeout = time.Nanosecond
	if err := s.recoverInterruptedChecks(ctx); err != nil {
		t.Fatal(err)
	}
	if client.StorageCheck.GetX(ctx, live.ID).Status != "running" {
		t.Fatal("changed deployment timeout expired an active old request")
	}
}

func TestStorageCheckSanitizePreservesCodesWithoutProviderText(t *testing.T) {
	_, _, s, _, _, _, _ := storageConnectionFixture(t)
	for _, cause := range []error{storage.ErrPayloadTooLarge, context.DeadlineExceeded} {
		wrapped := fmt.Errorf("provider secret: %w", cause)
		safe := s.sanitize(wrapped)
		if safe != cause {
			t.Fatalf("safe sentinel not preserved: %v", safe)
		}
	}
	if safe := s.sanitize(fmt.Errorf("provider secret: %w", context.Canceled)); safe != storage.ErrUnavailable {
		t.Fatal("client disconnect incorrectly reported durable cancellation")
	}
}

func TestStorageCheckFailureQueryableAndCleanupAccounted(t *testing.T) {
	ctx, client, s, u, c, sp, d := storageConnectionFixture(t)
	d.beforePut = func(string) {
		if client.StorageCheck.Query().CountX(ctx) != 1 || client.StorageCheckWrite.Query().CountX(ctx) == 0 {
			t.Fatal("external write before check identity and association")
		}
	}
	d.failDeleteResponse = true
	_, check, err := s.AuthorizeWithCheck(ctx, u.ID, c.ID, storageAuthorizeInput(c.ManagementGeneration))
	if !errors.Is(err, storage.ErrUnavailable) || check == nil || check.CheckID == 0 {
		t.Fatalf("failed check identity: %+v %v", check, err)
	}
	if check.AuthorizationActivated || check.CleanupStatus != "blocked" || check.AccountedBytes == 0 {
		t.Fatalf("failure facts: %+v", check)
	}
	if client.StorageCheckWrite.Query().Where(storagecheckwrite.CheckIDEQ(check.CheckID)).CountX(ctx) != 2 {
		t.Fatal("missing marker/probe links")
	}
	before := check.AccountedBytes
	d.beforePut = nil
	if err = s.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	check, err = s.GetCheck(ctx, u.ID, c.ID, check.CheckID)
	if err != nil || check.CleanupStatus != "done" || check.AccountedBytes >= before {
		t.Fatalf("reconciled check: %+v %v", check, err)
	}
	if check.AccountedBytes != client.StorageSpace.GetX(ctx, sp.ID).LiveBytes {
		t.Fatal("persistent marker cost lost")
	}
	calls := d.puts + d.deletes
	checks, err := s.ListChecks(ctx, u.ID, c.ID, 0, 50)
	if err != nil || len(checks) != 1 || calls != d.puts+d.deletes {
		t.Fatal("query must return recorded facts without IO")
	}
	other := client.User.Create().SetUsername("check-other").SetEmail("check-other@example.test").SetPasswordHash("unused").SaveX(ctx)
	if _, err = s.GetCheck(ctx, other.ID, c.ID, check.CheckID); !errors.Is(err, ErrForbidden) {
		t.Fatal("check owner isolation failed")
	}
}

func TestStorageCheckPartialSpacesAndReadonly(t *testing.T) {
	ctx, client, s, u, c, first, d := storageConnectionFixture(t)
	second, err := s.CreateSpace(ctx, u.ID, c.ID, CreateStorageSpaceInput{Name: "second", Bucket: "other", Prefix: "other"})
	if err != nil {
		t.Fatal(err)
	}
	s.factory = func(_ context.Context, _ *ent.StorageConnection, space *ent.StorageSpace, _ storageauth.S3Payload) (storage.Driver, error) {
		if space.ID == second.ID {
			return nil, storage.ErrPermission
		}
		return d, nil
	}
	_, check, err := s.AuthorizeWithCheck(ctx, u.ID, c.ID, storageAuthorizeInput(client.StorageConnection.GetX(ctx, c.ID).ManagementGeneration))
	if !errors.Is(err, storage.ErrPermission) || check == nil || len(check.Results) != 2 {
		t.Fatalf("partial results %+v %v", check, err)
	}
	if check.Results[0].SpaceID != first.ID || check.Results[0].Status != "completed" || check.Results[1].Status != "failed" {
		t.Fatalf("space results %+v", check.Results)
	}
	if check.AuthorizationActivated {
		t.Fatal("partial verification activated auth")
	}
}

func TestStorageCheckReadonlyAndFailedCandidateKeepCurrentAuthorization(t *testing.T) {
	ctx, client, s, u, c, _, d := storageConnectionFixture(t)
	active, _, err := s.AuthorizeWithCheck(ctx, u.ID, c.ID, storageAuthorizeInput(c.ManagementGeneration))
	if err != nil {
		t.Fatal(err)
	}
	puts, deletes := d.puts, d.deletes
	_, check, err := s.CheckWithResult(ctx, u.ID, c.ID, false, active.ManagementGeneration)
	if err != nil || check.CleanupStatus != "done" || d.puts != puts || d.deletes != deletes {
		t.Fatalf("readonly check mutated storage %+v %v", check, err)
	}
	d.failDeleteResponse = true
	_, check, err = s.AuthorizeWithCheck(ctx, u.ID, c.ID, storageAuthorizeInput(active.ManagementGeneration))
	if err == nil || check.AuthorizationActivated {
		t.Fatal("failed delete activated candidate")
	}
	current := client.StorageConnection.GetX(ctx, c.ID)
	if current.ActiveAuthGeneration != active.AuthGeneration || current.ManagementGeneration != active.ManagementGeneration {
		t.Fatal("failed check replaced active authorization")
	}
}
