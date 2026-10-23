package v015

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	"golang.org/x/crypto/bcrypt"
)

func TestReleaseConversionPreservesAccountsAndDefersReady(t *testing.T) {
	for _, mode := range []string{"serve", "local"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			source, sourcePath, hash := releaseFixture(t, mode)
			p := fixturePlan(t, source, mode)
			sourceProof, err := operation.Hash(ctx, sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			target := filepath.Join(directory, "target.db")
			if err := snapshot.SQLite(ctx, source, target); err != nil {
				t.Fatal(err)
			}
			cfg := config.ServerConfig{DataDir: directory, Database: config.DatabaseConfig{Driver: "sqlite", DSN: target + "?_pragma=foreign_keys(1)"}}
			db, client, err := snapshot.Open(ctx, &cfg, false)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := operation.Initialize(ctx, db, "sqlite", p); err != nil {
				t.Fatal(err)
			}
			for _, step := range []func(context.Context, *sql.DB, *operation.Plan) error{ConvertSchema, ConvertAccounts, ConvertHistory} {
				if err := step(ctx, db, p); err != nil {
					t.Fatal(err)
				}
			}
			user := client.User.GetX(ctx, 1)
			if user.PasswordHash != hash || user.AuthVersion != 1 || user.EmailVerifiedAt != nil || user.VerifiedEmailKey != "" || user.PasswordVerifiedHash != "" {
				t.Fatal("conversion changed authentication evidence")
			}
			if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte("original-password-123")); err != nil {
				t.Fatal("original password unavailable")
			}
			policy := client.LocalAccountPolicy.GetX(ctx, 1)
			if policy.SignupEmailRequirement != "required" || policy.SignupEnabled || policy.EmailLoginEnabled || !policy.PasswordLoginEnabled || !policy.UsernameLoginEnabled {
				t.Fatalf("policy changed: %+v", policy)
			}
			if client.InstanceInitialization.GetX(ctx, 1).DataVersion != 2 {
				t.Fatal("data version published before final verification")
			}
			injected := errors.New("cancel final verification after business migration")
			if err := Complete(ctx, db, p, func(*ent.Client) error { return injected }); !errors.Is(err, injected) {
				t.Fatal("final verification failure lost", err)
			}
			var markers int
			if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_schema WHERE name='linguaflow_auth_format'").Scan(&markers); err != nil || markers != 0 {
				t.Fatal("account format escaped final rollback", err)
			}
			steps, state, err := operation.Steps(ctx, db, p)
			if err != nil || state != "preparing" || client.InstanceInitialization.GetX(ctx, 1).DataVersion != 2 {
				t.Fatal("business version or readiness escaped final rollback", err)
			}
			for _, step := range steps {
				if step == "ready" {
					t.Fatal("failed final transaction wrote receipt")
				}
			}
			if err := Complete(ctx, db, p, func(c *ent.Client) error {
				if c.InstanceInitialization.GetX(ctx, 1).DataVersion != 3 {
					t.Fatal("final business migration absent")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			current, err := Probe(ctx, db, "sqlite")
			if err != nil || current.Format != "current" {
				t.Fatalf("current probe: %+v %v", current, err)
			}
			if err := ConvertAccounts(ctx, db, p); err != nil {
				t.Fatal("duplicate account step failed", err)
			}
			after, err := operation.Hash(ctx, sourcePath)
			if err != nil || after.SHA256 != sourceProof.SHA256 {
				t.Fatal("conversion wrote source")
			}
		})
	}
}
