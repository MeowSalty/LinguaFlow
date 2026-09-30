package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/instanceinitialization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/refreshtoken"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/timeutil"
)

const InitializationVersion = 1

var (
	ErrInstanceUninitialized      = errors.New("instance is not initialized")
	ErrInstanceIncomplete         = errors.New("instance initialization state is incomplete")
	errInitializationWonElsewhere = errors.New("another initializer committed")
)

type InitializationService struct{ client *ent.Client }

func NewInitializationService(client *ent.Client) *InitializationService {
	return &InitializationService{client: client}
}

// IsEmpty checks whether startup may prepare dependencies for a new instance.
// This is read-only and does not reserve initialization; Initialize rechecks
// business data inside its database-protected transaction before committing.
func (s *InitializationService) IsEmpty(ctx context.Context) (bool, error) {
	exists, err := s.client.InstanceInitialization.Query().Exist(ctx)
	if err != nil || exists {
		return false, err
	}
	for _, check := range initializationBusinessChecks(s.client) {
		exists, err = check(ctx)
		if err != nil || exists {
			return false, err
		}
	}
	return true, nil
}

func instanceMode(mode string) (instanceinitialization.Mode, error) {
	switch mode {
	case config.ModeServer, "serve":
		return instanceinitialization.ModeServe, nil
	case config.ModeLocal:
		return instanceinitialization.ModeLocal, nil
	default:
		return "", ErrInvalidInput
	}
}

// Initialize only applies bootstrap to an empty database. Existing instances are
// validated without modifying identities or policy, even when no admins remain.
func (s *InitializationService) Initialize(ctx context.Context, mode string, input config.BootstrapInput) (*ent.User, error) {
	wantMode, err := instanceMode(mode)
	if err != nil {
		return nil, err
	}
	if wantMode == instanceinitialization.ModeLocal && input.Admin != nil {
		return nil, fmt.Errorf("bootstrap.admin does not apply to local mode")
	}
	local, err := s.Validate(ctx, mode)
	if !errors.Is(err, ErrInstanceUninitialized) {
		return local, err
	}
	var username, email, passwordHash string
	var inputErr error
	if wantMode == instanceinitialization.ModeLocal {
		var secret [32]byte
		if _, err := rand.Read(secret[:]); err != nil {
			return nil, err
		}
		passwordHash, err = hashPassword(hex.EncodeToString(secret[:]))
		if err != nil {
			return nil, err
		}
		username, email = "local", "local@linguaflow.local"
	} else if input.Admin == nil {
		inputErr = errors.New("uninitialized serve instance requires bootstrap.admin or admin initialize with administrator inputs")
	} else {
		username, email = normalizeIdentity(input.Admin.Username), normalizeIdentity(input.Admin.Email)
		if username == "" || !strings.Contains(email, "@") {
			inputErr = ErrInvalidInput
		} else if err := validateNewPassword(input.Admin.Password); err != nil {
			inputErr = err
		} else {
			passwordHash, inputErr = hashPassword(input.Admin.Password)
		}
	}
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		// First write: unique ID serializes concurrent initializers on both engines.
		if _, err := tx.InstanceInitialization.Create().SetID(1).SetVersion(InitializationVersion).SetMode(wantMode).Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				return errInitializationWonElsewhere
			}
			return err
		}
		for table, exists := range initializationBusinessChecks(tx) {
			populated, err := exists(ctx)
			if err != nil {
				return err
			}
			if populated {
				return fmt.Errorf("%w: data exists in %s without a completed initialization marker; use a separate database or explicitly rebuild development data", ErrInstanceIncomplete, table)
			}
		}
		if inputErr != nil {
			return inputErr
		}
		if _, err := tx.SystemSetting.Create().SetKey(SettingRegistrationEnabled).SetValue(strconv.FormatBool(input.RegistrationEnabled)).Save(ctx); err != nil {
			return err
		}
		account, err := tx.User.Create().SetUsername(username).SetEmail(email).SetPasswordHash(passwordHash).SetRole(SystemRoleAdmin).SetActive(true).Save(ctx)
		if err != nil {
			return err
		}
		if wantMode == instanceinitialization.ModeLocal {
			if err := tx.InstanceInitialization.UpdateOneID(1).SetLocalUserID(account.ID).Exec(ctx); err != nil {
				return err
			}
		}
		return tx.ActivityLog.Create().SetVisibilityScope(activitylog.VisibilityScopeUnknown).
			SetAction("instance.initialize").SetResourceType("instance").SetResourceID(1).
			SetMetadata(map[string]any{"mode": string(wantMode), "version": InitializationVersion, "administrator_id": account.ID, "registration_enabled": input.RegistrationEnabled}).Exec(ctx)
	})
	if err != nil && !errors.Is(err, errInitializationWonElsewhere) {
		return nil, err
	}
	return s.Validate(ctx, mode)
}

func (s *InitializationService) Validate(ctx context.Context, mode string) (*ent.User, error) {
	wantMode, err := instanceMode(mode)
	if err != nil {
		return nil, err
	}
	rows, err := s.client.InstanceInitialization.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrInstanceUninitialized
	}
	if len(rows) != 1 || rows[0].ID != 1 || rows[0].Version != InitializationVersion || rows[0].Mode != wantMode {
		return nil, fmt.Errorf("%w: unsupported initialization version or instance mode; use a separate data directory", ErrInstanceIncomplete)
	}
	if _, err := NewSettingsService(s.client).Get(ctx); err != nil {
		return nil, err
	}
	if wantMode == instanceinitialization.ModeServe {
		if rows[0].LocalUserID != nil {
			return nil, ErrInstanceIncomplete
		}
		return nil, nil
	}
	if rows[0].LocalUserID == nil {
		return nil, ErrInstanceIncomplete
	}
	local, err := s.client.User.Get(ctx, *rows[0].LocalUserID)
	if ent.IsNotFound(err) {
		return nil, fmt.Errorf("%w: local identity missing", ErrInstanceIncomplete)
	}
	if err != nil {
		return nil, err
	}
	if !local.Active || local.Role != SystemRoleAdmin {
		return nil, fmt.Errorf("%w: local identity is not an active administrator", ErrInstanceIncomplete)
	}
	return local, nil
}

// Keep this inventory in sync with ent/migrate.Tables; its completeness is tested.
func initializationBusinessChecks(c *ent.Client) map[string]func(context.Context) (bool, error) {
	return map[string]func(context.Context) (bool, error){
		"activity_logs": c.ActivityLog.Query().Exist, "backends": c.Backend.Query().Exist,
		"bootstrap_prompt_templates": c.BootstrapPromptTemplate.Query().Exist,
		"execution_plan_templates":   c.ExecutionPlanTemplate.Query().Exist, "execution_profiles": c.ExecutionProfile.Query().Exist,
		"glossary_entries": c.GlossaryEntry.Query().Exist, "jobs": c.Job.Query().Exist,
		"job_resources": c.JobResource.Query().Exist, "job_rounds": c.JobRound.Query().Exist,
		"job_round_segments": c.JobRoundSegment.Query().Exist, "organizations": c.Organization.Query().Exist,
		"org_memberships": c.OrgMembership.Query().Exist, "projects": c.Project.Query().Exist,
		"prune_prompt_templates": c.PrunePromptTemplate.Query().Exist, "refresh_tokens": c.RefreshToken.Query().Exist,
		"resources": c.Resource.Query().Exist, "segments": c.Segment.Query().Exist,
		"segment_revisions": c.SegmentRevision.Query().Exist, "sse_events": c.SSEEvent.Query().Exist,
		"sync_tasks": c.SyncTask.Query().Exist, "system_settings": c.SystemSetting.Query().Exist,
		"tm_entries": c.TMEntry.Query().Exist, "translation_prompt_templates": c.TranslationPromptTemplate.Query().Exist,
		"usage_records": c.UsageRecord.Query().Exist, "users": c.User.Query().Exist,
	}
}

// MaintainAdministrator is an explicit deployment-side action. It cannot claim
// an uninitialized instance or alter its registration policy.
func (s *InitializationService) MaintainAdministrator(ctx context.Context, input AdminCreateUserInput, recoverExisting bool) (*ent.User, error) {
	username, email := normalizeIdentity(input.Username), normalizeIdentity(input.Email)
	if username == "" || (!recoverExisting && !strings.Contains(email, "@")) {
		return nil, ErrInvalidInput
	}
	if err := validateNewPassword(input.Password); err != nil {
		return nil, err
	}
	passwordHash, err := hashPassword(input.Password)
	if err != nil {
		return nil, err
	}
	var result *ent.User
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		// Serializes maintenance actions and establishes SQLite's write snapshot.
		if _, err := tx.InstanceInitialization.Update().Where(instanceinitialization.IDEQ(1)).SetUpdatedAt(timeutil.NowUTC()).Save(ctx); err != nil {
			return err
		}
		if _, err := NewInitializationService(tx).Validate(ctx, config.ModeServer); err != nil {
			return err
		}
		action := "admin.create"
		if recoverExisting {
			existing, err := tx.User.Query().Where(user.UsernameEQ(username)).Only(ctx)
			if err != nil {
				return err
			}
			result, err = tx.User.UpdateOneID(existing.ID).SetPasswordHash(passwordHash).SetRole(SystemRoleAdmin).SetActive(true).Save(ctx)
			if err != nil {
				return err
			}
			if _, err := tx.RefreshToken.Update().Where(refreshtoken.HasUserWith(user.IDEQ(existing.ID)), refreshtoken.RevokedAtIsNil()).SetRevokedAt(timeutil.NowUTC()).Save(ctx); err != nil {
				return err
			}
			action = "admin.recover"
		} else {
			var err error
			result, err = tx.User.Create().SetUsername(username).SetEmail(email).SetPasswordHash(passwordHash).SetRole(SystemRoleAdmin).SetActive(true).Save(ctx)
			if ent.IsConstraintError(err) {
				return ErrUserExists
			}
			if err != nil {
				return err
			}
		}
		return tx.ActivityLog.Create().SetVisibilityScope(activitylog.VisibilityScopeUnknown).SetAction(action).SetResourceType("user").SetResourceID(result.ID).
			SetMetadata(map[string]any{"source": "deployment_command", "administrator_id": result.ID}).Exec(ctx)
	})
	if err != nil {
		return nil, err
	}
	return result.Unwrap(), nil
}
