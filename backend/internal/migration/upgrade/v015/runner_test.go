package v015

import (
	"context"
	"errors"
	"github.com/golang-jwt/jwt/v5"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/api"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func fixtureDeployment(t *testing.T, mode string) (PlanOptions, string) {
	t.Helper()
	db, path, _ := releaseFixture(t, mode)
	_ = db
	directory := filepath.Dir(path)
	configPath := filepath.Join(directory, "source.yml")
	text := "kind: server\nversion: 1\nserver:\n  data_dir: " + filepath.ToSlash(directory) + "\n  jwt_secret: '12345678901234567890123456789012'\n"
	text += "  credentials:\n    keyring_file: " + filepath.ToSlash(filepath.Join(directory, "credentials-keyring.json")) + "\n"
	text += "  storage:\n    initialization:\n      capacity_bytes: null\n      logical_limit_bytes: null\n"
	keys, _, err := credential.GenerateKeyring()
	if err != nil {
		t.Fatal(err)
	}
	data, err := credential.EncodeKeyring(keys)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := credential.PublishPrivateFile(filepath.Join(directory, "credentials-keyring.json"), data); err != nil {
		t.Fatal(err)
	}
	if mode == "serve" {
		text += "  database:\n    driver: sqlite\n    dsn: " + filepath.ToSlash(path) + "\n"
	} else {
		// Local configuration always uses its standard file name.
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		newPath := filepath.Join(directory, "linguaflow.db")
		if err := os.Rename(path, newPath); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(configPath, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	return PlanOptions{Inputs: config.ServerInputs{Mode: mode, ConfigPath: &configPath, Environment: map[string]string{}}, Directory: filepath.Join(parent, "operation"), TargetDataDir: filepath.Join(parent, "target")}, directory
}

func TestRunnerReleaseRoundTripAndReadOnlyVerification(t *testing.T) {
	for _, mode := range []string{"serve", "local"} {
		t.Run(mode, func(t *testing.T) {
			options, sourceDir := fixtureDeployment(t, mode)
			ctx := context.Background()
			p, err := NewPlan(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			before, err := operation.Hash(ctx, filepath.Join(sourceDir, "source.yml"))
			if err != nil {
				t.Fatal(err)
			}
			report, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true})
			if err != nil {
				t.Fatal(err)
			}
			if !report.Verified || report.Phase != "completed" {
				t.Fatalf("report: %+v", report)
			}
			for range 2 {
				if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true}); err != nil {
					t.Fatal("repeat apply", err)
				}
			}
			if _, err := VerifyOperation(ctx, options.Directory); err != nil {
				t.Fatal("verification", err)
			}
			after, err := operation.Hash(ctx, filepath.Join(sourceDir, "source.yml"))
			if err != nil || before.SHA256 != after.SHA256 {
				t.Fatal("deployment input changed")
			}
			deployment := filepath.Join(options.TargetDataDir, "deployment.yaml")
			resolved, err := config.ResolveServerConfig(config.ServerInputs{Mode: mode, ConfigPath: &deployment, Environment: map[string]string{}})
			if err != nil {
				t.Fatal("generated deployment", err)
			}
			db, client, err := database.Open(ctx, &resolved.Config)
			if err != nil {
				t.Fatal("runtime startup", err)
			}
			defer db.Close()
			if mode == "local" {
				marker, err := client.InstanceInitialization.Get(ctx, 1)
				if err != nil || marker.LocalUserID == nil || *marker.LocalUserID != p.Accounts[0].UserID {
					t.Fatal("local identity was replaced")
				}
			}
			if mode == "serve" {
				auth := service.NewAuthService(client, service.AuthConfigFromServer(resolved.Config), service.NewSettingsService(client))
				session, err := auth.Login(ctx, service.LoginInput{Identifier: p.Accounts[0].Username, Password: "original-password-123"})
				if err != nil || session.User.ID != 1 || session.User.Role != "admin" {
					t.Fatal("original administrator login", err)
				}
				if _, err := auth.Login(ctx, service.LoginInput{Identifier: "admin@example.invalid", Password: "original-password-123"}); !errors.Is(err, service.ErrInvalidCredentials) {
					t.Fatal("email login enabled")
				}
				claims := jwt.MapClaims{"uid": 1, "username": "OriginalAdmin", "role": "admin", "iss": resolved.Config.JWTIssuer, "sub": "user:1", "exp": time.Now().Add(time.Hour).Unix()}
				oldToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(resolved.Config.JWTSecret))
				if err != nil {
					t.Fatal(err)
				}
				server, err := api.NewServer(&resolved.Config, resolved.CredentialKeys, slog.New(slog.NewTextHandler(io.Discard, nil)), db, client, config.ModeServer, nil)
				if err != nil {
					t.Fatal("runtime API initialization", err)
				}
				defer server.Shutdown(ctx)
				request := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
				request.Header.Set("Authorization", "Bearer "+oldToken)
				response := httptest.NewRecorder()
				server.GetCurrentUser(response, request)
				if response.Code != http.StatusUnauthorized {
					t.Fatalf("old token status=%d", response.Code)
				}
				request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", strings.NewReader(`{"refresh_token":"original-refresh-token"}`))
				request.Header.Set("Content-Type", "application/json")
				response = httptest.NewRecorder()
				server.RefreshAuth(response, request)
				if response.Code != http.StatusUnauthorized {
					t.Fatalf("old refresh token status=%d", response.Code)
				}
			}
		})
	}
}

func TestRunnerResumesDatabaseCommitAndPublishBeforeJournal(t *testing.T) {
	for _, phase := range []string{"backup", "staging", "before_schema", "schema_commit", "accounts_commit", "history_commit", "storage_commit", "before_ready", "ready_commit", "before_report", "before_publish", "publish"} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			options, _ := fixtureDeployment(t, "serve")
			if _, err := NewPlan(ctx, options); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected boundary cancellation")
			fired := false
			_, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true, Fault: func(at string) error {
				if at == phase {
					fired = true
					return injected
				}
				return nil
			}})
			if !fired || !errors.Is(err, injected) {
				t.Fatalf("phase %s: %v", phase, err)
			}
			if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true}); err != nil {
				t.Fatal("resume", err)
			}
			if _, err := VerifyOperation(ctx, options.Directory); err != nil {
				t.Fatal(err)
			}
		})
	}
}
