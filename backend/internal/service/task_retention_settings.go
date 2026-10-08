package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
)

const (
	SettingTaskRetention           = "task_retention"
	MaxTaskRetentionRevision int64 = 1<<53 - 1
)

type TaskRetentionPolicy struct {
	Enabled       bool  `json:"enabled"`
	RetentionDays int   `json:"retention_days"`
	Revision      int64 `json:"revision"`
}

type TaskRetentionPatch struct {
	Enabled          bool
	RetentionDays    int
	ExpectedRevision int64
}

func defaultTaskRetention() TaskRetentionPolicy {
	return TaskRetentionPolicy{Enabled: false, RetentionDays: 30, Revision: 1}
}

func validTaskRetentionPatch(p TaskRetentionPatch) bool {
	return p.RetentionDays >= 1 && p.RetentionDays <= 3650 && p.ExpectedRevision >= 1 && p.ExpectedRevision <= MaxTaskRetentionRevision
}

func parseTaskRetention(value string) (TaskRetentionPolicy, error) {
	var fields struct {
		Enabled       *bool  `json:"enabled"`
		RetentionDays *int   `json:"retention_days"`
		Revision      *int64 `json:"revision"`
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fields); err != nil {
		return TaskRetentionPolicy{}, fmt.Errorf("invalid task_retention: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return TaskRetentionPolicy{}, errors.New("invalid task_retention trailing data")
	}
	if fields.Enabled == nil || fields.RetentionDays == nil || fields.Revision == nil || !validTaskRetentionPatch(TaskRetentionPatch{RetentionDays: *fields.RetentionDays, ExpectedRevision: *fields.Revision}) {
		return TaskRetentionPolicy{}, errors.New("invalid task_retention fields")
	}
	return TaskRetentionPolicy{Enabled: *fields.Enabled, RetentionDays: *fields.RetentionDays, Revision: *fields.Revision}, nil
}

func readTaskRetention(ctx context.Context, client *ent.Client) (TaskRetentionPolicy, error) {
	row, err := client.SystemSetting.Query().Where(systemsetting.KeyEQ(SettingTaskRetention)).Only(ctx)
	if err != nil {
		return TaskRetentionPolicy{}, fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)
	}
	policy, err := parseTaskRetention(row.Value)
	if err != nil {
		return TaskRetentionPolicy{}, fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)
	}
	return policy, nil
}

func (s *SettingsService) TaskRetention(ctx context.Context) (TaskRetentionPolicy, error) {
	return readTaskRetention(ctx, s.client)
}

// OnTaskRetentionChanged installs a nonblocking notification invoked only after commit.
func (s *SettingsService) OnTaskRetentionChanged(callback func()) {
	s.callbackMu.Lock()
	s.onTaskRetentionChanged = callback
	s.callbackMu.Unlock()
}

func lockTaskRetention(ctx context.Context, client *ent.Client) error {
	return lockSetting(ctx, client, SettingTaskRetention)
}

func lockSetting(ctx context.Context, client *ent.Client, key string) error {
	// Fixed SQL works with both supported dialects and bypasses update defaults.
	var statement string
	switch key {
	case SettingTaskRetention:
		statement = "UPDATE system_settings SET value = value WHERE key = 'task_retention'"
	case SettingRegistrationEnabled:
		statement = "UPDATE system_settings SET value = value WHERE key = 'registration_enabled'"
	default:
		return ErrInvalidInput
	}
	result, err := client.ExecContext(ctx, statement)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSettingsUnavailable, err)
	}
	if count != 1 {
		return fmt.Errorf("%w: missing %s", ErrSettingsUnavailable, key)
	}
	return nil
}

func saveTaskRetention(ctx context.Context, client *ent.Client, policy TaskRetentionPolicy) error {
	data, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	_, err = client.SystemSetting.Update().Where(systemsetting.KeyEQ(SettingTaskRetention)).SetValue(string(data)).Save(ctx)
	return err
}

func taskRetentionAudit(before, after TaskRetentionPolicy) map[string]any {
	return map[string]any{"before_enabled": before.Enabled, "before_retention_days": before.RetentionDays, "before_revision": before.Revision,
		"after_enabled": after.Enabled, "after_retention_days": after.RetentionDays, "after_revision": after.Revision}
}
