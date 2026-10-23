package v015

import (
	"path/filepath"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/snapshot"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestReleasedUsernameSwapAndEmailNullAcceptance(t *testing.T) {
	for _, mode := range []string{"serve", "local"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			options, _ := fixtureDeployment(t, mode)
			resolved, err := configSource(options)
			if err != nil {
				t.Fatal(err)
			}
			db, _, err := snapshot.Open(ctx, &resolved.Config, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, user := range []struct {
				id          int
				name, email string
			}{{2, "Alice", "alice@example.invalid"}, {3, "Bob", "bob@example.invalid"}} {
				if _, err := db.ExecContext(ctx, "INSERT INTO users(id,created_at,updated_at,username,password_hash,email,role,active) SELECT ?,created_at,updated_at,?,password_hash,?,'user',true FROM users WHERE id=1", user.id, user.name, user.email); err != nil {
					db.Close()
					t.Fatal(err)
				}
			}
			db.Close()
			options.MappingPath = filepath.Join(t.TempDir(), "account-map.json")
			if _, err := credential.PublishPrivateFile(options.MappingPath, []byte(`{"2":{"username":"BOB","email":null},"3":{"username":"ALICE","email":"Admin@EXAMPLE.invalid"}}`)); err != nil {
				t.Fatal(err)
			}
			p, err := NewPlan(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Accounts) != 3 || p.Accounts[1].Email != nil {
				t.Fatal("mapping did not freeze complete users or explicit null")
			}
			if _, err := Run(ctx, RunOptions{Directory: options.Directory, Offline: true}); err != nil {
				t.Fatal(err)
			}
			deployment := filepath.Join(options.TargetDataDir, "deployment.yaml")
			targetConfig, err := config.ResolveServerConfig(config.ServerInputs{Mode: mode, ConfigPath: &deployment, Environment: map[string]string{}})
			if err != nil {
				t.Fatal(err)
			}
			target, client, err := database.Open(ctx, &targetConfig.Config)
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			first, second := client.User.GetX(ctx, 2), client.User.GetX(ctx, 3)
			if first.Username != "bob" || first.Email != "" || second.Username != "alice" || second.Email != "admin@example.invalid" {
				t.Fatal("username exchange or email null changed")
			}
			var nullEmail bool
			if err := target.QueryRowContext(ctx, "SELECT email IS NULL FROM users WHERE id=2").Scan(&nullEmail); err != nil || !nullEmail {
				t.Fatal("email null was stored as an empty string", err)
			}
			auth := service.NewAuthService(client, service.AuthConfigFromServer(targetConfig.Config), service.NewSettingsService(client))
			for _, account := range p.Accounts {
				session, err := auth.Login(ctx, service.LoginInput{Identifier: account.Username, Password: "original-password-123"})
				if err != nil || session.User.ID != account.UserID {
					t.Fatal("original password no longer follows original ID", err)
				}
			}
		})
	}
}
