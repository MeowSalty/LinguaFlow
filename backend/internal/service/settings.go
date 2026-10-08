package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
)

const SettingRegistrationEnabled = "registration_enabled"

var (
	ErrSettingsUnavailable = errors.New("system settings unavailable")
	ErrSettingsConflict    = errors.New("settings_conflict")
)

type SystemSettings struct {
	RegistrationEnabled bool                `json:"registration_enabled"`
	TaskRetention       TaskRetentionPolicy `json:"task_retention"`
}

type SettingsPatch struct {
	RegistrationEnabled *bool
	TaskRetention       *TaskRetentionPatch
}

type RegistrationPolicy interface {
	RegistrationEnabled(context.Context) (bool, error)
}

type SettingsService struct {
	client                 *ent.Client
	callbackMu             sync.RWMutex
	onTaskRetentionChanged func()
}

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
	var settings SystemSettings
	registrationFound, retentionFound := false, false
	for _, row := range rows {
		switch row.Key {
		case SettingRegistrationEnabled:
			settings.RegistrationEnabled, err = parseRegistrationPolicy(row.Value)
			registrationFound = true
		case SettingTaskRetention:
			settings.TaskRetention, err = parseTaskRetention(row.Value)
			retentionFound = true
		case storagePolicyKey:
			continue
		default:
			return SystemSettings{}, errors.New("system settings contain unsupported keys")
		}
		if err != nil {
			return SystemSettings{}, err
		}
	}
	if !registrationFound || !retentionFound {
		return SystemSettings{}, errors.New("required system settings are missing")
	}
	return settings, nil
}

func parseRegistrationPolicy(value string) (bool, error) {
	switch value {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("registration_enabled is not a canonical boolean")
	}
}

func readRegistrationPolicy(ctx context.Context, client *ent.Client) (bool, error) {
	row, err := client.SystemSetting.Query().Where(systemsetting.KeyEQ(SettingRegistrationEnabled)).Only(ctx)
	if err != nil {
		return false, err
	}
	return parseRegistrationPolicy(row.Value)
}

func (s *SettingsService) RegistrationEnabled(ctx context.Context) (bool, error) {
	value, err := readRegistrationPolicy(ctx, s.client)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)
	}
	return value, nil
}

// Update retains the registration-only contract of existing service callers.
func (s *SettingsService) Update(ctx context.Context, actorID int, input SystemSettings) (SystemSettings, error) {
	return s.Patch(ctx, actorID, SettingsPatch{RegistrationEnabled: &input.RegistrationEnabled})
}

func (s *SettingsService) Patch(ctx context.Context, actorID int, input SettingsPatch) (SystemSettings, error) {
	if input.RegistrationEnabled == nil && input.TaskRetention == nil {
		return SystemSettings{}, ErrInvalidInput
	}
	if input.TaskRetention != nil && !validTaskRetentionPatch(*input.TaskRetention) {
		return SystemSettings{}, ErrInvalidInput
	}
	var result SystemSettings
	var policyChanged bool
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		policyChanged = false
		// All multi-domain updates lock registration before task retention.
		if input.RegistrationEnabled != nil {
			if err := lockSetting(ctx, tx, SettingRegistrationEnabled); err != nil {
				return err
			}
		}
		if input.TaskRetention != nil {
			if err := lockTaskRetention(ctx, tx); err != nil {
				return err
			}
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
		result = before
		if input.TaskRetention != nil {
			if before.TaskRetention.Revision != input.TaskRetention.ExpectedRevision {
				return ErrSettingsConflict
			}
			policyChanged = before.TaskRetention.Enabled != input.TaskRetention.Enabled || before.TaskRetention.RetentionDays != input.TaskRetention.RetentionDays
			if policyChanged && before.TaskRetention.Revision == MaxTaskRetentionRevision {
				return ErrSettingsConflict
			}
		}
		if input.RegistrationEnabled != nil && before.RegistrationEnabled != *input.RegistrationEnabled {
			result.RegistrationEnabled = *input.RegistrationEnabled
			if _, err := tx.SystemSetting.Update().Where(systemsetting.KeyEQ(SettingRegistrationEnabled)).SetValue(strconv.FormatBool(result.RegistrationEnabled)).Save(ctx); err != nil {
				return err
			}
			if err := recordAuditEvent(ctx, tx, AuditEvent{ActorUserID: actor.ID, Action: "admin.settings.update", ResourceType: "system_settings", Metadata: map[string]any{"before_registration_enabled": before.RegistrationEnabled, "after_registration_enabled": result.RegistrationEnabled}}); err != nil {
				return err
			}
		}
		if policyChanged {
			result.TaskRetention = TaskRetentionPolicy{Enabled: input.TaskRetention.Enabled, RetentionDays: input.TaskRetention.RetentionDays, Revision: before.TaskRetention.Revision + 1}
			if err := saveTaskRetention(ctx, tx, result.TaskRetention); err != nil {
				return err
			}
			if err := recordAuditEvent(ctx, tx, AuditEvent{ActorUserID: actor.ID, Action: "admin.task_retention.update", ResourceType: "system_settings", Metadata: taskRetentionAudit(before.TaskRetention, result.TaskRetention)}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return SystemSettings{}, err
	}
	if policyChanged {
		s.callbackMu.RLock()
		callback := s.onTaskRetentionChanged
		s.callbackMu.RUnlock()
		if callback != nil {
			callback()
		}
	}
	return result, nil
}
