package service

import (
	"context"
	"encoding/json"
	"entgo.io/ent/dialect/sql"
	"errors"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/migrate"
	"strconv"

	"github.com/MeowSalty/LinguaFlow/backend/internal/accountidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/activitylog"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/instanceinitialization"
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

// IsEmpty 检查启动是否可为新实例准备依赖。
// 该检查只读，不预留初始化；Initialize 在提交前
// 会在其受数据库保护的事务内重新检查业务数据。
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

// Initialize 仅在空数据库上应用 bootstrap。已存在的实例
// 会被校验但不修改身份或策略，即使已无任何管理员。
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
		username, inputErr = accountidentity.NormalizeUsername("local")
		if inputErr == nil {
			inputErr = accountidentity.ValidateAllocation(username, accountidentity.Local)
		}
	} else if input.Admin == nil {
		inputErr = errors.New("uninitialized serve instance requires bootstrap.admin or admin initialize with administrator inputs")
	} else {
		username, inputErr = accountidentity.NormalizeUsername(input.Admin.Username)
		if inputErr == nil {
			inputErr = accountidentity.ValidateAllocation(username, accountidentity.Administrator)
		}
		if inputErr == nil {
			email, inputErr = accountidentity.NormalizeEmail(input.Admin.Email)
		}
		if inputErr != nil {
			inputErr = ErrInvalidInput
		} else if err := validateNewPassword(input.Admin.Password); err != nil {
			inputErr = err
		} else {
			passwordHash, inputErr = hashPassword(input.Admin.Password)
		}
	}
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		// 首次写入：唯一 ID 在两个引擎上串行化并发初始化者。
		if _, err := tx.InstanceInitialization.Create().SetID(1).SetVersion(InitializationVersion).SetDataVersion(CurrentDataVersion).SetMode(wantMode).Save(ctx); err != nil {
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
		retention, err := json.Marshal(defaultTaskRetention())
		if err != nil {
			return err
		}
		if err := tx.SystemSetting.Create().SetKey(SettingTaskRetention).SetValue(string(retention)).Exec(ctx); err != nil {
			return err
		}
		if err := initializePasswordConnection(ctx, tx); err != nil {
			return err
		}
		create := tx.User.Create().SetUsername(username).SetRole(SystemRoleAdmin).SetActive(true)
		if email != "" {
			create.SetEmail(email)
		}
		if passwordHash != "" {
			create.SetPasswordHash(passwordHash)
		}
		account, err := create.Save(ctx)
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
	// Initialization depends on its original registration policy and identity.
	// Task retention faults are reported by that domain, not as lost ownership.
	if _, err := NewSettingsService(s.client).RegistrationEnabled(ctx); err != nil {
		return nil, err
	}
	if _, err := readPasswordPolicy(ctx, s.client); err != nil {
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

// The generated schema is the only business-table registry, shared by preflight and transaction recheck.
func initializationBusinessChecks(c *ent.Client) map[string]func(context.Context) (bool, error) {
	checks := make(map[string]func(context.Context) (bool, error), len(migrate.Tables)-1)
	for _, table := range migrate.Tables {
		if table.Name == "instance_initializations" {
			continue
		}
		name := table.Name
		checks[name] = func(ctx context.Context) (bool, error) {
			query, args := sql.Dialect(c.DialectName()).SelectExpr(sql.Expr("1")).From(sql.Dialect(c.DialectName()).Table(name)).Limit(1).Query()
			rows, err := c.QueryContext(ctx, query, args...)
			if err != nil {
				return false, err
			}
			exists := rows.Next()
			return exists, errors.Join(rows.Err(), rows.Close())
		}
	}
	return checks
}

// MaintainAdministrator 是显式的部署侧操作。它不能认领
// 未初始化的实例，也不能更改其注册策略。
func (s *InitializationService) MaintainAdministrator(ctx context.Context, input AdminCreateUserInput, recoverExisting bool) (*ent.User, error) {
	username, err := accountidentity.NormalizeUsername(input.Username)
	if err != nil {
		return nil, ErrInvalidInput
	}
	var email string
	if !recoverExisting {
		if err := accountidentity.ValidateAllocation(username, accountidentity.Administrator); err != nil {
			return nil, ErrInvalidInput
		}
		email, err = accountidentity.NormalizeEmail(input.Email)
		if err != nil {
			return nil, ErrInvalidInput
		}
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
		// 串行化维护操作，并建立 SQLite 的写快照。
		if err := lockAuthenticationConfiguration(ctx, tx); err != nil {
			return err
		}
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
			if err := lockAuthUser(ctx, tx, existing.ID); err != nil {
				return err
			}
			if err := revokeUserSessions(ctx, tx, existing.ID, timeutil.NowUTC()); err != nil {
				return err
			}
			result, err = tx.User.UpdateOneID(existing.ID).SetPasswordHash(passwordHash).SetRole(SystemRoleAdmin).SetActive(true).Save(ctx)
			if err != nil {
				return err
			}
			action = "admin.recover"
		} else {
			var err error
			create := tx.User.Create().SetUsername(username).SetPasswordHash(passwordHash).SetRole(SystemRoleAdmin).SetActive(true)
			if email != "" {
				create.SetEmail(email)
			}
			result, err = create.Save(ctx)
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
