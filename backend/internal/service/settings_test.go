package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestSettingsFailClosedAndTyped(t *testing.T) {
	ctx := context.Background()
	for _, value := range []string{"", "true", "false", "TRUE", "0", "invalid"} {
		t.Run(value, func(t *testing.T) {
			c := testClient(t)
			if value != "" {
				c.SystemSetting.Create().SetKey(SettingRegistrationEnabled).SetValue(value).SaveX(ctx)
			}
			settings := NewSettingsService(c)
			auth := NewAuthService(c, AuthConfig{Secret: []byte("test-secret"), Issuer: "test", AccessTokenTTL: time.Hour, RefreshTokenTTL: time.Hour}, settings)
			session, err := auth.Register(ctx, RegisterInput{Username: "first", Email: "first@test.invalid", Password: "password-123"})
			switch value {
			case "true":
				if err != nil || session.User.Role != SystemRoleUser {
					t.Fatalf("registration: %v %+v", err, session)
				}
			case "false":
				if !errors.Is(err, ErrRegistrationClosed) {
					t.Fatalf("error=%v", err)
				}
			default:
				if !errors.Is(err, ErrSettingsUnavailable) {
					t.Fatalf("error=%v", err)
				}
			}
		})
	}
}

func TestSettingsReadFailureNeverOpensRegistration(t *testing.T) {
	c := testClient(t)
	_ = c.Close()
	auth := NewAuthService(c, AuthConfig{}, NewSettingsService(c))
	if _, err := auth.Register(context.Background(), RegisterInput{}); !errors.Is(err, ErrSettingsUnavailable) {
		t.Fatalf("error=%v", err)
	}
}

func TestSettingsAuditFailureRollsBackPolicy(t *testing.T) {
	ctx := context.Background()
	c := testClient(t)
	c.SystemSetting.Create().SetKey(SettingRegistrationEnabled).SetValue("true").SaveX(ctx)
	c.SystemSetting.Create().SetKey(SettingTaskRetention).SetValue(`{"enabled":false,"retention_days":30,"revision":1}`).SaveX(ctx)
	u := c.User.Create().SetUsername("admin").SetEmail("admin@test.invalid").SetPasswordHash("unused").SetRole(SystemRoleAdmin).SaveX(ctx)
	failure := errors.New("audit unavailable")
	c.ActivityLog.Use(func(ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(context.Context, ent.Mutation) (ent.Value, error) { return nil, failure })
	})
	if _, err := NewSettingsService(c).Update(ctx, u.ID, SystemSettings{}); !errors.Is(err, failure) {
		t.Fatalf("error=%v", err)
	}
	settings, err := NewSettingsService(c).Get(ctx)
	if err != nil || !settings.RegistrationEnabled {
		t.Fatalf("policy partially committed: %+v %v", settings, err)
	}
}
