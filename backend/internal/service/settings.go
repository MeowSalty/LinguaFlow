package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

const SettingRegistrationEnabled = "registration_enabled"

var ErrSettingsUnavailable = errors.New("system settings unavailable")

type SystemSettings struct {
	RegistrationEnabled bool `json:"registration_enabled"`
}

type RegistrationPolicy interface {
	RegistrationEnabled(context.Context) (bool, error)
}

type SettingsService struct{ client *ent.Client }

func NewSettingsService(client *ent.Client) *SettingsService { return &SettingsService{client: client} }

func (s *SettingsService) Get(ctx context.Context) (SystemSettings, error) {
	settings, err := readSettings(ctx, s.client)
	if err != nil {
		return SystemSettings{}, fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)
	}
	return settings, nil
}

func readSettings(ctx context.Context, client *ent.Client) (SystemSettings, error) {
	rows, err := client.SystemSetting.Query().All(ctx)
	if err != nil {
		return SystemSettings{}, err
	}
	filtered := rows[:0]
	for _, row := range rows {
		if row.Key != storagePolicyKey {
			filtered = append(filtered, row)
		}
	}
	rows = filtered
	if len(rows) != 1 || rows[0].Key != SettingRegistrationEnabled {
		return SystemSettings{}, errors.New("system settings are missing or contain unsupported keys")
	}
	switch rows[0].Value {
	case "true":
		return SystemSettings{RegistrationEnabled: true}, nil
	case "false":
		return SystemSettings{}, nil
	default:
		return SystemSettings{}, errors.New("registration_enabled is not a canonical boolean")
	}
}

func (s *SettingsService) RegistrationEnabled(ctx context.Context) (bool, error) {
	settings, err := s.Get(ctx)
	return settings.RegistrationEnabled, err
}

func (s *SettingsService) Update(ctx context.Context, actorID int, input SystemSettings) (SystemSettings, error) {
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		// 在读取现有策略的旧值或权限之前先将其锁定。
		if _, err := tx.SystemSetting.Update().Where(systemsetting.KeyEQ(SettingRegistrationEnabled)).
			SetDescription("Public registration policy").Save(ctx); err != nil {
			return err
		}
		actor, err := tx.User.Query().Where(user.IDEQ(actorID), user.ActiveEQ(true), user.RoleEQ(SystemRoleAdmin)).Only(ctx)
		if ent.IsNotFound(err) {
			return ErrForbidden
		}
		if err != nil {
			return err
		}
		before, err := readSettings(ctx, tx)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)
		}
		if _, err := tx.SystemSetting.Update().Where(systemsetting.KeyEQ(SettingRegistrationEnabled)).
			SetValue(strconv.FormatBool(input.RegistrationEnabled)).Save(ctx); err != nil {
			return err
		}
		return tx.ActivityLog.Create().SetActorID(actor.ID).SetVisibilityScope(activitylog.VisibilityScopeUnknown).
			SetAction("admin.settings.update").SetResourceType("system_settings").
			SetMetadata(map[string]any{"before": before, "after": input}).Exec(ctx)
	})
	if err != nil {
		return SystemSettings{}, err
	}
	return input, nil
}
