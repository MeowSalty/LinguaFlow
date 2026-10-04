package service

import (
	"context"
	"encoding/json"
	"time"

	"entgo.io/ent/dialect/sql"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/organization"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/systemsetting"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

const storagePolicyKey = "storage_policy"

type StoragePolicy struct {
	Mode                     string `json:"mode"`
	DefaultChoice            string `json:"default_choice"`
	Generation               int64  `json:"generation"`
	LogicalLimitBytes        int64  `json:"logical_limit_bytes"`
	ConfigurationNeedsUpdate bool   `json:"configuration_needs_update,omitempty" readOnly:"true"`
}

func storagePolicy(ctx context.Context, client *ent.Client) (StoragePolicy, error) {
	row, err := client.SystemSetting.Query().Where(systemsetting.KeyEQ(storagePolicyKey)).Only(ctx)
	if ent.IsNotFound(err) {
		return StoragePolicy{Mode: "site_only", DefaultChoice: "site", LogicalLimitBytes: 100 << 30}, nil
	}
	if err != nil {
		return StoragePolicy{}, err
	}
	var p StoragePolicy
	if err = json.Unmarshal([]byte(row.Value), &p); err != nil {
		return p, err
	}
	p.ConfigurationNeedsUpdate = (p.Mode == "site_only" && p.DefaultChoice != "site") || (p.Mode == "user_required" && p.DefaultChoice != "user")
	switch p.Mode {
	case "site_only":
		p.DefaultChoice = "site"
	case "user_required":
		p.DefaultChoice = "user"
	case "both":
		if p.DefaultChoice != "site" && p.DefaultChoice != "user" {
			return p, ErrStoragePolicy
		}
	default:
		return p, ErrStoragePolicy
	}
	return p, nil
}
func (s *StorageService) Policy(ctx context.Context) (StoragePolicy, error) {
	return storagePolicy(ctx, s.client)
}
func (s *StorageService) SetPolicy(ctx context.Context, actor int, p StoragePolicy) (StoragePolicy, error) {
	// This read-only hint is derived from stored configuration, never persisted.
	p.ConfigurationNeedsUpdate = false
	if p.Mode != "site_only" && p.Mode != "both" && p.Mode != "user_required" {
		return p, ErrInvalidInput
	}
	if p.DefaultChoice != "site" && p.DefaultChoice != "user" {
		return p, ErrInvalidInput
	}
	if p.LogicalLimitBytes <= 0 || p.Generation < 0 || (p.Mode == "site_only" && p.DefaultChoice != "site") || (p.Mode == "user_required" && p.DefaultChoice != "user") {
		return p, ErrInvalidInput
	}
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		ok, e := tx.User.Query().Where(user.IDEQ(actor), user.ActiveEQ(true), user.RoleEQ(SystemRoleAdmin)).Exist(ctx)
		if e != nil {
			return e
		}
		if !ok {
			return ErrForbidden
		}
		if !s.cfg.Enabled && p.Mode != "site_only" {
			return ErrStoragePolicy
		}
		old, e := storagePolicy(ctx, tx)
		if e != nil {
			return e
		}
		if old.Generation != p.Generation {
			return ErrStorageConflict
		}
		p.Generation++
		b, e := json.Marshal(p)
		if e != nil {
			return e
		}
		row, e := tx.SystemSetting.Query().Where(systemsetting.KeyEQ(storagePolicyKey)).Only(ctx)
		if ent.IsNotFound(e) {
			return tx.SystemSetting.Create().SetKey(storagePolicyKey).SetValue(string(b)).Exec(ctx)
		}
		if e != nil {
			return e
		}
		n, e := tx.SystemSetting.Update().Where(systemsetting.IDEQ(row.ID), systemsetting.ValueEQ(row.Value)).SetValue(string(b)).Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrStorageConflict
		}
		return nil
	})
	return p, err
}

func (s *StorageService) allowedTarget(ctx context.Context, tx *ent.Client, p *ent.Project, id int) error {
	return s.validateStorageTarget(ctx, tx, p, id, -1, false)
}

func (s *StorageService) selectSpace(ctx context.Context, tx *ent.Client, p *ent.Project, requested *int) (int, error) {
	if requested != nil {
		if err := s.allowedTarget(ctx, tx, p, *requested); err != nil {
			return 0, err
		}
		return *requested, nil
	}
	policy, err := storagePolicy(ctx, tx)
	if err != nil {
		return 0, err
	}
	if policy.Mode == "user_required" || policy.Mode == "both" && policy.DefaultChoice == "user" {
		return 0, ErrStoragePolicy
	}
	if s.defaultSpaceID == 0 {
		return 0, ErrStoragePolicy
	}
	if err = s.allowedTarget(ctx, tx, p, s.defaultSpaceID); err != nil {
		return 0, err
	}
	return s.defaultSpaceID, nil
}

// InstallSiteSpace 只登记部署身份，不扫描也不移动旧版文件。
func (s *StorageService) InstallSiteSpace(ctx context.Context, backendID string, d storage.Driver, initiallyVerified ...bool) (*ent.StorageSpace, error) {
	verified := true
	if len(initiallyVerified) > 0 {
		verified = initiallyVerified[0]
	}
	var sp *ent.StorageSpace
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		conn, e := tx.StorageConnection.Query().Where(storageconnection.BackendIDEQ(backendID), storageconnection.OwnerKindEQ(storageconnection.OwnerKindSite)).Only(ctx)
		if ent.IsNotFound(e) {
			conn, e = tx.StorageConnection.Create().SetName(backendID).SetDriver(storageconnection.DriverLocal).SetBackendID(backendID).Save(ctx)
		}
		if e != nil {
			return e
		}
		sp, e = tx.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(conn.ID)).Only(ctx)
		if ent.IsNotFound(e) {
			sp, e = tx.StorageSpace.Create().SetConnectionID(conn.ID).SetName(backendID).SetIdentity(generateUniqueID()).SetMarkerNonce(generateUniqueID()).SetVerified(verified).Save(ctx)
		}
		return e
	})
	if err != nil {
		return nil, err
	}
	s.RegisterDriver(sp.ID, d)
	s.defaultSpaceID = sp.ID
	return sp, nil
}

func (s *StorageService) Bind(ctx context.Context, actor, projectID, spaceID int, generation int64) error {
	return s.projects.mutateProject(ctx, actor, projectID, func(tx *ent.Client, p *ent.Project) error {
		if p.StorageGeneration != generation {
			return ErrStorageConflict
		}
		if p.StorageState != "active" || s.maintenance {
			return ErrStorageMaintenance
		}
		if e := storageEmptyProject(ctx, tx, projectID); e != nil {
			return e
		}
		if e := s.allowedTarget(ctx, tx, p, spaceID); e != nil {
			return e
		}
		n, e := tx.Project.Update().Where(project.IDEQ(projectID), project.StorageGenerationEQ(generation)).SetStorageSpaceID(spaceID).AddStorageGeneration(1).Save(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrStorageConflict
		}
		return nil
	})
}

func (s *StorageService) logicalAdmission(ctx context.Context, tx *ent.Client, p *ent.Project, size int64) error {
	if size < 0 {
		return ErrInvalidInput
	}
	kind, owner := storageProjectOwner(p)
	// 同一归属者下的不同项目共享同一个配额与准入锁。
	var err error
	switch kind {
	case "user":
		_, err = tx.User.Update().Where(user.IDEQ(owner)).SetUpdatedAt(time.Now().UTC()).Save(ctx)
	case "org":
		_, err = tx.Organization.Update().Where(organization.IDEQ(owner)).SetUpdatedAt(time.Now().UTC()).Save(ctx)
	default:
		return ErrStoragePolicy
	}
	if err != nil {
		return err
	}
	policy, err := storagePolicy(ctx, tx)
	if err != nil {
		return err
	}
	var totals []struct {
		Total int64 `json:"total"`
	}
	err = tx.Blob.Query().Where(blob.OwnerKindEQ(blob.OwnerKind(kind)), blob.OwnerIDEQ(owner), blob.StatusIn(blob.StatusReady, blob.StatusPending)).Aggregate(func(selector *sql.Selector) string {
		return sql.As("COALESCE("+sql.Sum(selector.C(blob.FieldSize))+", 0)", "total")
	}).Scan(ctx, &totals)
	if err != nil {
		return err
	}
	var total int64
	if len(totals) != 0 {
		total = totals[0].Total
	}
	if size > policy.LogicalLimitBytes-total {
		return storage.ErrLimit
	}
	return nil
}
