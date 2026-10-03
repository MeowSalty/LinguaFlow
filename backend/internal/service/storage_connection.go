package service

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageauthversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storagenet"
)

var ErrStorageCrypto = errors.New("storage_crypto_unavailable")

// StorageDriverFactory 只接收目标连接显式提供的凭据。
// 其 HTTP 传输必须基于 cfg.NetworkPolicy 并使用 storagenet。
type StorageDriverFactory func(context.Context, *ent.StorageConnection, *ent.StorageSpace, storageauth.S3Payload) (storage.Driver, error)

type StorageConnectionService struct {
	client       *ent.Client
	keys         *credential.Keyring
	cfg          config.StorageConfig
	factory      StorageDriverFactory
	mu           sync.Mutex
	activeWrites map[int]bool
}

func NewStorageConnectionService(client *ent.Client, keys *credential.Keyring, cfg config.StorageConfig, factory StorageDriverFactory) *StorageConnectionService {
	return &StorageConnectionService{client: client, keys: keys, cfg: cfg, factory: factory, activeWrites: map[int]bool{}}
}

type CreateStorageConnectionInput struct {
	Name      string `json:"name"`
	Scope     string `json:"scope"`
	OwnerID   int    `json:"owner_id"`
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	PathStyle bool   `json:"path_style"`
}
type CreateStorageSpaceInput struct {
	Name          string `json:"name"`
	Bucket        string `json:"bucket"`
	Prefix        string `json:"prefix"`
	CapacityBytes int64  `json:"capacity_bytes"`
}
type AuthorizeStorageInput struct {
	Payload                      storageauth.S3Payload `json:"-"`
	WriteCheck                   bool                  `json:"write_check"`
	ExpiresAt                    *time.Time            `json:"expires_at,omitempty"`
	ExpectedManagementGeneration int64                 `json:"expected_management_generation"`
}
type StorageConnectionRecord struct {
	ID                   int        `json:"id"`
	Name                 string     `json:"name"`
	Scope                string     `json:"scope"`
	OwnerID              int        `json:"owner_id"`
	Driver               string     `json:"driver"`
	Endpoint             string     `json:"endpoint"`
	Region               string     `json:"region"`
	PathStyle            bool       `json:"path_style"`
	Status               string     `json:"status"`
	Health               string     `json:"health"`
	HasAuth              bool       `json:"has_auth"`
	AuthGeneration       int64      `json:"auth_generation"`
	ManagementGeneration int64      `json:"management_generation"`
	CheckedAt            *time.Time `json:"checked_at,omitempty"`
}
type StorageSpaceRecord struct {
	ID                   int    `json:"id"`
	ConnectionID         int    `json:"connection_id"`
	Name                 string `json:"name"`
	Scope                string `json:"scope"`
	OwnerID              int    `json:"owner_id"`
	Bucket               string `json:"bucket"`
	Prefix               string `json:"prefix"`
	Status               string `json:"status"`
	Verified             bool   `json:"verified"`
	Versioned            bool   `json:"versioned"`
	ManagementGeneration int64  `json:"management_generation"`
	CapacityBytes        int64  `json:"capacity_bytes"`
	ReservedBytes        int64  `json:"reserved_bytes"`
	CandidateBytes       int64  `json:"candidate_bytes"`
	LiveBytes            int64  `json:"live_bytes"`
	PendingDeleteBytes   int64  `json:"pending_delete_bytes"`
}

func storageConnectionRecord(c *ent.StorageConnection) *StorageConnectionRecord {
	return &StorageConnectionRecord{c.ID, c.Name, string(c.OwnerKind), c.OwnerID, string(c.Driver), c.Endpoint, c.Region, c.PathStyle, string(c.Status), c.Health, c.ActiveAuthGeneration > 0, c.ActiveAuthGeneration, c.ManagementGeneration, c.CheckedAt}
}
func storageSpaceRecord(s *ent.StorageSpace) *StorageSpaceRecord {
	return &StorageSpaceRecord{s.ID, s.ConnectionID, s.Name, string(s.OwnerKind), s.OwnerID, s.Bucket, s.Prefix, string(s.Status), s.Verified, s.Versioned, s.ManagementGeneration, s.CapacityBytes, s.ReservedBytes, s.CandidateBytes, s.LiveBytes, s.PendingDeleteBytes}
}

func (s *StorageConnectionService) requireOwner(ctx context.Context, client *ent.Client, actor int, scope string, owner int) error {
	active, err := client.User.Query().Where(user.IDEQ(actor), user.ActiveEQ(true)).Exist(ctx)
	if err != nil {
		return err
	}
	if !active {
		return ErrForbidden
	}
	if scope == ScopeUser && owner == actor {
		return nil
	}
	if scope == ScopeOrg {
		_, err := requireOrganizationMembership(ctx, client, actor, owner, OrgRoleAdmin)
		return err
	}
	if scope == "site" && owner == 0 {
		ok, err := client.User.Query().Where(user.IDEQ(actor), user.RoleEQ(SystemRoleAdmin)).Exist(ctx)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return ErrForbidden
}

func (s *StorageConnectionService) authorized(ctx context.Context, client *ent.Client, actor, id int) (*ent.StorageConnection, error) {
	c, err := client.StorageConnection.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, storage.ErrUnavailable
	}
	if err != nil {
		return nil, err
	}
	if err = s.requireOwner(ctx, client, actor, string(c.OwnerKind), c.OwnerID); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *StorageConnectionService) Create(ctx context.Context, actor int, in CreateStorageConnectionInput) (*StorageConnectionRecord, error) {
	if !s.cfg.Enabled {
		return nil, ErrStoragePolicy
	}
	if s.cfg.Maintenance {
		return nil, ErrStorageMaintenance
	}
	if in.Scope != ScopeUser && in.Scope != ScopeOrg {
		return nil, ErrForbidden
	}
	if err := s.requireOwner(ctx, s.client, actor, in.Scope, in.OwnerID); err != nil {
		return nil, err
	}
	endpoint, err := storagenet.NormalizeEndpoint(in.Endpoint)
	if err != nil {
		return nil, ErrInvalidInput
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 200 || strings.TrimSpace(in.Region) == "" || len(in.Region) > 100 {
		return nil, ErrInvalidInput
	}
	c, err := s.client.StorageConnection.Create().SetName(strings.TrimSpace(in.Name)).SetOwnerKind(storageconnection.OwnerKind(in.Scope)).SetOwnerID(in.OwnerID).SetDriver(storageconnection.DriverS3).SetEndpoint(endpoint).SetRegion(in.Region).SetPathStyle(in.PathStyle).SetAuthSource(storageconnection.AuthSourceStored).Save(ctx)
	if err != nil {
		return nil, err
	}
	return storageConnectionRecord(c), nil
}

func (s *StorageConnectionService) List(ctx context.Context, actor int, scope string, owner int) ([]*StorageConnectionRecord, error) {
	if err := s.requireOwner(ctx, s.client, actor, scope, owner); err != nil {
		return nil, err
	}
	rows, err := s.client.StorageConnection.Query().Where(storageconnection.OwnerKindEQ(storageconnection.OwnerKind(scope)), storageconnection.OwnerIDEQ(owner)).Order(ent.Asc(storageconnection.FieldID)).Limit(1000).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*StorageConnectionRecord, 0, len(rows))
	for _, c := range rows {
		out = append(out, storageConnectionRecord(c))
	}
	return out, nil
}
func (s *StorageConnectionService) Get(ctx context.Context, actor, id int) (*StorageConnectionRecord, error) {
	c, err := s.authorized(ctx, s.client, actor, id)
	if err != nil {
		return nil, err
	}
	return storageConnectionRecord(c), nil
}

func (s *StorageConnectionService) Spaces(ctx context.Context, actor, connectionID int) ([]*StorageSpaceRecord, error) {
	if _, err := s.authorized(ctx, s.client, actor, connectionID); err != nil {
		return nil, err
	}
	rows, err := s.client.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(connectionID)).Order(ent.Asc(storagespace.FieldID)).Limit(1000).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*StorageSpaceRecord, 0, len(rows))
	for _, sp := range rows {
		out = append(out, storageSpaceRecord(sp))
	}
	return out, nil
}

func (s *StorageConnectionService) CreateSpace(ctx context.Context, actor, connectionID int, in CreateStorageSpaceInput) (*StorageSpaceRecord, error) {
	if !s.cfg.Enabled {
		return nil, ErrStoragePolicy
	}
	if s.cfg.Maintenance {
		return nil, ErrStorageMaintenance
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 200 || in.Bucket == "" || len(in.Bucket) > 63 || strings.ContainsAny(in.Bucket, "/\\\x00 \t\r\n") || strings.ContainsAny(in.Prefix, "\\\x00\r\n") || strings.HasPrefix(in.Prefix, "/") || len(in.Prefix) > 512 || in.CapacityBytes < 0 {
		return nil, ErrInvalidInput
	}
	for _, part := range strings.Split(in.Prefix, "/") {
		if part == "." || part == ".." {
			return nil, ErrInvalidInput
		}
	}
	in.Prefix = strings.TrimSuffix(in.Prefix, "/")
	if in.CapacityBytes == 0 {
		in.CapacityBytes = s.cfg.Limits.CapacityBytes
	}
	if in.CapacityBytes > s.cfg.Limits.CapacityBytes {
		return nil, storage.ErrLimit
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out *ent.StorageSpace
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		c, err := s.authorized(ctx, tx, actor, connectionID)
		if err != nil {
			return err
		}
		if c.AuthSource != storageconnection.AuthSourceStored {
			return ErrForbidden
		}
		if c.Status != storageconnection.StatusEnabled {
			return storage.ErrPermission
		}
		// 端点别名并不能证明是不同的物理存储桶。即使端点名称不同，
		// 也要求托管根路径互不相交。
		spaces, err := tx.StorageSpace.Query().Where(storagespace.BucketEQ(in.Bucket)).All(ctx)
		if err != nil {
			return err
		}
		for _, existing := range spaces {
			if storagePrefixesOverlap(existing.Prefix, in.Prefix) {
				return ErrStorageConflict
			}
		}
		out, err = tx.StorageSpace.Create().SetConnectionID(c.ID).SetName(strings.TrimSpace(in.Name)).SetIdentity(generateUniqueID()).SetMarkerNonce(generateUniqueID()).SetBucket(in.Bucket).SetPrefix(in.Prefix).SetOwnerKind(storagespace.OwnerKind(c.OwnerKind)).SetOwnerID(c.OwnerID).SetCapacityBytes(in.CapacityBytes).Save(ctx)
		if err != nil {
			return err
		}
		// 使任何仍在校验旧空间集合的授权候选失效。
		return tx.StorageConnection.UpdateOneID(c.ID).AddManagementGeneration(1).Exec(ctx)
	})
	if err != nil {
		return nil, err
	}
	return storageSpaceRecord(out), nil
}

func storagePrefixesOverlap(a, b string) bool {
	return a == b || a == "" || b == "" || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func (s *StorageConnectionService) SetStatus(ctx context.Context, actor, id int, status string, generation int64) (*StorageConnectionRecord, error) {
	if status != "enabled" && status != "disabled" {
		return nil, ErrInvalidInput
	}
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		c, err := s.authorized(ctx, tx, actor, id)
		if err != nil {
			return err
		}
		if c.AuthSource != storageconnection.AuthSourceStored {
			return ErrForbidden
		}
		n, err := tx.StorageConnection.Update().Where(storageconnection.IDEQ(id), storageconnection.ManagementGenerationEQ(generation)).SetStatus(storageconnection.Status(status)).AddManagementGeneration(1).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStorageConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, actor, id)
}

func (s *StorageConnectionService) SetSpaceStatus(ctx context.Context, actor, id int, status string, generation int64) (*StorageSpaceRecord, error) {
	if status != "active" && status != "read_only" && status != "disabled" {
		return nil, ErrInvalidInput
	}
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		sp, err := tx.StorageSpace.Get(ctx, id)
		if err != nil {
			return err
		}
		if _, err = s.authorized(ctx, tx, actor, sp.ConnectionID); err != nil {
			return err
		}
		n, err := tx.StorageSpace.Update().Where(storagespace.IDEQ(id), storagespace.ManagementGenerationEQ(generation)).SetStatus(storagespace.Status(status)).AddManagementGeneration(1).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStorageConflict
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sp, err := s.client.StorageSpace.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return storageSpaceRecord(sp), nil
}

func storageAuthIdentity(c *ent.StorageConnection, generation int64) storageauth.Identity {
	return storageauth.Identity{ConnectionID: c.ID, Scope: string(c.OwnerKind), OwnerID: c.OwnerID, Driver: string(c.Driver), Endpoint: c.Endpoint, AuthGeneration: generation}
}
func storageCiphertext(a *ent.StorageAuthVersion) credential.Ciphertext {
	return credential.Ciphertext{Version: 1, KeyID: a.KeyID, Nonce: a.Nonce, Data: a.Ciphertext}
}

func (s *StorageConnectionService) Authorize(ctx context.Context, actor, id int, in AuthorizeStorageInput) (*StorageConnectionRecord, error) {
	if !s.cfg.Enabled && in.WriteCheck {
		return nil, ErrStoragePolicy
	}
	if s.cfg.Maintenance {
		return nil, ErrStorageMaintenance
	}
	if err := in.Payload.Validate(); err != nil {
		return nil, ErrInvalidInput
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now()) {
		return nil, ErrInvalidInput
	}
	var c *ent.StorageConnection
	var candidate *ent.StorageAuthVersion
	var spaces []*ent.StorageSpace
	s.mu.Lock()
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		var err error
		c, err = s.authorized(ctx, tx, actor, id)
		if err != nil {
			return err
		}
		if c.AuthSource != storageconnection.AuthSourceStored || c.Status != storageconnection.StatusEnabled {
			return storage.ErrPermission
		}
		if c.ManagementGeneration != in.ExpectedManagementGeneration {
			return ErrStorageConflict
		}
		spaces, err = tx.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(id)).Order(ent.Asc(storagespace.FieldID)).Limit(101).All(ctx)
		if err != nil {
			return err
		}
		if len(spaces) == 0 || len(spaces) > 100 {
			return ErrInvalidInput
		}
		last, err := tx.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(id)).Order(ent.Desc(storageauthversion.FieldGeneration)).First(ctx)
		var next int64 = 1
		if err == nil {
			next = last.Generation + 1
		} else if !ent.IsNotFound(err) {
			return err
		}
		if next <= 0 {
			return ErrStorageConflict
		}
		value, err := storageauth.EncryptS3(s.keys, storageAuthIdentity(c, next), in.Payload)
		if err != nil {
			return ErrStorageCrypto
		}
		candidate, err = tx.StorageAuthVersion.Create().SetConnectionID(id).SetGeneration(next).SetPayloadVersion(storageauth.PayloadVersion).SetAadVersion(storageauth.AADVersion).SetKeyID(value.KeyID).SetNonce(value.Nonce).SetCiphertext(value.Data).SetNillableExpiresAt(in.ExpiresAt).Save(ctx)
		return err
	})
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	checked := map[int]storage.Capabilities{}
	for _, sp := range spaces {
		if sp.Status == storagespace.StatusDisabled {
			continue
		}
		if in.WriteCheck && sp.Status != storagespace.StatusActive {
			err = storage.ErrPermission
			break
		}
		var d storage.Driver
		d, err = s.candidateDriver(ctx, c, sp, candidate)
		if err == nil {
			var caps storage.Capabilities
			caps, err = s.verifySpace(ctx, actor, c, sp, candidate, d, in.WriteCheck)
			checked[sp.ID] = caps
		}
		if err != nil {
			break
		}
	}
	if len(checked) == 0 && err == nil {
		err = storage.ErrPermission
	}
	if err != nil {
		s.retireCandidate(ctx, candidate.ID)
		return nil, s.sanitize(err)
	}
	err = withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		if candidate.ExpiresAt != nil && !candidate.ExpiresAt.After(time.Now()) {
			return storage.ErrAuthRequired
		}
		if err := s.requireOwner(ctx, tx, actor, string(c.OwnerKind), c.OwnerID); err != nil {
			return err
		}
		n, err := tx.StorageConnection.Update().Where(storageconnection.IDEQ(c.ID), storageconnection.ManagementGenerationEQ(c.ManagementGeneration), storageconnection.ActiveAuthGenerationEQ(c.ActiveAuthGeneration), storageconnection.StatusEQ(storageconnection.StatusEnabled)).SetActiveAuthGeneration(candidate.Generation).SetHealth("available").SetCheckedAt(time.Now().UTC()).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStorageConflict
		}
		n, err = tx.StorageAuthVersion.Update().Where(storageauthversion.IDEQ(candidate.ID), storageauthversion.StatusEQ(storageauthversion.StatusCandidate)).SetStatus(storageauthversion.StatusActive).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStorageConflict
		}
		for _, sp := range spaces {
			caps, ok := checked[sp.ID]
			if !ok {
				continue
			}
			n, err = tx.StorageSpace.Update().Where(storagespace.IDEQ(sp.ID), storagespace.ManagementGenerationEQ(sp.ManagementGeneration)).SetVerified(true).SetVersioned(caps.Versioned).Save(ctx)
			if err != nil {
				return err
			}
			if n != 1 {
				return ErrStorageConflict
			}
		}
		_, err = tx.StorageAuthVersion.Update().Where(storageauthversion.ConnectionIDEQ(c.ID), storageauthversion.GenerationNEQ(candidate.Generation), storageauthversion.StatusEQ(storageauthversion.StatusActive)).SetStatus(storageauthversion.StatusRetired).Save(ctx)
		return err
	})
	if err != nil {
		s.retireCandidate(ctx, candidate.ID)
		return nil, err
	}
	return s.Get(ctx, actor, id)
}

func (s *StorageConnectionService) retireCandidate(ctx context.Context, id int) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_, _ = s.client.StorageAuthVersion.Update().Where(storageauthversion.IDEQ(id), storageauthversion.StatusEQ(storageauthversion.StatusCandidate)).SetStatus(storageauthversion.StatusRetired).Save(cleanup)
}

func (s *StorageConnectionService) Revoke(ctx context.Context, actor, id int, generation int64) (*StorageConnectionRecord, error) {
	err := withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		c, err := s.authorized(ctx, tx, actor, id)
		if err != nil {
			return err
		}
		if c.AuthSource != storageconnection.AuthSourceStored {
			return ErrForbidden
		}
		n, err := tx.StorageConnection.Update().Where(storageconnection.IDEQ(id), storageconnection.ManagementGenerationEQ(generation)).SetActiveAuthGeneration(0).SetHealth("auth_required").AddManagementGeneration(1).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStorageConflict
		}
		_, err = tx.StorageAuthVersion.Update().Where(storageauthversion.ConnectionIDEQ(id), storageauthversion.StatusIn(storageauthversion.StatusActive, storageauthversion.StatusCandidate)).SetStatus(storageauthversion.StatusRevoked).Save(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, actor, id)
}

// Reencrypt 只覆盖本领域，保留 generation 与吊销事实。
// 维护命令会把本报告与现有的 LLM 报告合并输出。
func (s *StorageConnectionService) Reencrypt(ctx context.Context) (changed, failed int, err error) {
	if s.keys == nil {
		return 0, 0, ErrStorageCrypto
	}
	last := 0
	for {
		rows, e := s.client.StorageAuthVersion.Query().Where(storageauthversion.IDGT(last)).Order(ent.Asc(storageauthversion.FieldID)).Limit(100).All(ctx)
		if e != nil {
			return changed, failed, e
		}
		if len(rows) == 0 {
			return changed, failed, nil
		}
		for _, a := range rows {
			last = a.ID
			if ctx.Err() != nil {
				return changed, failed, ctx.Err()
			}
			if a.AadVersion != storageauth.AADVersion || a.PayloadVersion != storageauth.PayloadVersion {
				failed++
				continue
			}
			c, e := s.client.StorageConnection.Get(ctx, a.ConnectionID)
			if e != nil {
				failed++
				continue
			}
			if a.KeyID == s.keys.ActiveKeyID() {
				// key ID 匹配并不能证明这些字节可解密。
				if _, e = storageauth.DecryptS3(s.keys, storageAuthIdentity(c, a.Generation), storageCiphertext(a)); e != nil {
					failed++
				}
				continue
			}
			value, e := storageauth.Reencrypt(s.keys, storageAuthIdentity(c, a.Generation), storageCiphertext(a))
			if e != nil {
				failed++
				continue
			}
			n, e := s.client.StorageAuthVersion.Update().Where(storageauthversion.IDEQ(a.ID), storageauthversion.KeyIDEQ(a.KeyID), storageauthversion.CiphertextEQ(a.Ciphertext), storageauthversion.NonceEQ(a.Nonce)).SetKeyID(value.KeyID).SetNonce(value.Nonce).SetCiphertext(value.Data).Save(ctx)
			if e != nil || n != 1 {
				failed++
			} else {
				changed++
			}
		}
	}
}

func (s *StorageConnectionService) sanitize(err error) error {
	for _, safe := range []error{storage.ErrNotFound, storage.ErrCorrupt, storage.ErrPermission, storage.ErrAuthRequired, storage.ErrLimit, storage.ErrUnsupported, ErrStorageConflict, ErrStorageCrypto, ErrStorageMaintenance, ErrForbidden} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return storage.ErrUnavailable
}

func (s *StorageConnectionService) loadAuth(ctx context.Context, c *ent.StorageConnection, generation int64, allowed storageauthversion.Status) (*ent.StorageAuthVersion, storageauth.S3Payload, error) {
	a, err := s.client.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(c.ID), storageauthversion.GenerationEQ(generation), storageauthversion.StatusEQ(allowed)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, storageauth.S3Payload{}, storage.ErrAuthRequired
	}
	if err != nil {
		return nil, storageauth.S3Payload{}, err
	}
	if a.ExpiresAt != nil && !a.ExpiresAt.After(time.Now()) {
		return nil, storageauth.S3Payload{}, storage.ErrAuthRequired
	}
	if a.AadVersion != storageauth.AADVersion || a.PayloadVersion != storageauth.PayloadVersion {
		return nil, storageauth.S3Payload{}, ErrStorageCrypto
	}
	payload, err := storageauth.DecryptS3(s.keys, storageAuthIdentity(c, a.Generation), storageCiphertext(a))
	if err != nil {
		return nil, storageauth.S3Payload{}, ErrStorageCrypto
	}
	return a, payload, nil
}

// ResolveDriver 是内部入口：调用方已完成针对项目的操作者鉴权。
// 每次操作都会重新检查当前生效的授权。
func (s *StorageConnectionService) ResolveDriver(ctx context.Context, spaceID int, write bool) (storage.Driver, error) {
	if write && s.cfg.Maintenance {
		return nil, ErrStorageMaintenance
	}
	sp, err := s.client.StorageSpace.Get(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	if !sp.Verified {
		return nil, storage.ErrAuthRequired
	}
	if _, _, err = s.activeDriver(ctx, sp.ID, write); err != nil {
		return nil, err
	}
	return &storageGuardedDriver{service: s, spaceID: sp.ID}, nil
}

func (s *StorageConnectionService) activeDriver(ctx context.Context, spaceID int, write bool) (storage.Driver, *ent.StorageSpace, error) {
	if write && !s.cfg.Enabled {
		return nil, nil, ErrStoragePolicy
	}
	sp, err := s.client.StorageSpace.Get(ctx, spaceID)
	if err != nil {
		return nil, nil, err
	}
	c, err := s.client.StorageConnection.Get(ctx, sp.ConnectionID)
	if err != nil {
		return nil, nil, err
	}
	if c.Status != storageconnection.StatusEnabled || sp.Status == storagespace.StatusDisabled {
		return nil, nil, storage.ErrPermission
	}
	if write && (s.cfg.Maintenance || sp.Status != storagespace.StatusActive) {
		return nil, nil, ErrStorageMaintenance
	}
	if c.AuthSource == storageconnection.AuthSourceDeployment {
		d, err := s.deploymentDriver(ctx, c, sp)
		return d, sp, err
	}
	if c.ActiveAuthGeneration == 0 {
		return nil, nil, storage.ErrAuthRequired
	}
	_, payload, err := s.loadAuth(ctx, c, c.ActiveAuthGeneration, storageauthversion.StatusActive)
	if err != nil {
		return nil, nil, err
	}
	if s.factory == nil {
		return nil, nil, storage.ErrUnavailable
	}
	d, err := s.factory(ctx, c, sp, payload)
	return d, sp, err
}

func (s *StorageConnectionService) candidateDriver(ctx context.Context, c *ent.StorageConnection, sp *ent.StorageSpace, a *ent.StorageAuthVersion) (storage.Driver, error) {
	check := func(ctx context.Context, write bool) (storage.Driver, error) {
		current, err := s.client.StorageConnection.Get(ctx, c.ID)
		if err != nil {
			return nil, err
		}
		space, err := s.client.StorageSpace.Get(ctx, sp.ID)
		if err != nil {
			return nil, err
		}
		if current.Status != storageconnection.StatusEnabled || current.ManagementGeneration != c.ManagementGeneration || current.ActiveAuthGeneration != c.ActiveAuthGeneration || space.ManagementGeneration != sp.ManagementGeneration || space.Status == storagespace.StatusDisabled {
			return nil, ErrStorageConflict
		}
		if write && (s.cfg.Maintenance || space.Status != storagespace.StatusActive) {
			return nil, storage.ErrPermission
		}
		_, payload, err := s.loadAuth(ctx, current, a.Generation, storageauthversion.StatusCandidate)
		if err != nil {
			return nil, err
		}
		if s.factory == nil {
			return nil, storage.ErrUnavailable
		}
		return s.factory(ctx, current, space, payload)
	}
	if _, err := check(ctx, false); err != nil {
		return nil, err
	}
	return &storageGuardedDriver{check: check, deleteCheck: func(ctx context.Context) (storage.Driver, error) {
		if s.cfg.Maintenance {
			return nil, ErrStorageMaintenance
		}
		return check(ctx, false)
	}}, nil
}

type storageGuardedDriver struct {
	service     *StorageConnectionService
	spaceID     int
	check       func(context.Context, bool) (storage.Driver, error)
	deleteCheck func(context.Context) (storage.Driver, error)
}

func (d *storageGuardedDriver) resolve(ctx context.Context, write bool) (storage.Driver, error) {
	if d.check != nil {
		return d.check(ctx, write)
	}
	driver, _, err := d.service.activeDriver(ctx, d.spaceID, write)
	return driver, err
}
func (d *storageGuardedDriver) PutNew(ctx context.Context, key string, r io.Reader, n int64) (storage.Object, error) {
	v, err := d.resolve(ctx, true)
	if err != nil {
		return storage.Object{}, err
	}
	return v.PutNew(ctx, key, r, n)
}
func (d *storageGuardedDriver) Open(ctx context.Context, o storage.Object) (io.ReadCloser, error) {
	v, err := d.resolve(ctx, false)
	if err != nil {
		return nil, err
	}
	return v.Open(ctx, o)
}
func (d *storageGuardedDriver) Stat(ctx context.Context, o storage.Object) (storage.Object, error) {
	v, err := d.resolve(ctx, false)
	if err != nil {
		return storage.Object{}, err
	}
	return v.Stat(ctx, o)
}
func (d *storageGuardedDriver) Delete(ctx context.Context, o storage.Object) error {
	var v storage.Driver
	var err error
	if d.deleteCheck != nil {
		v, err = d.deleteCheck(ctx)
	} else if d.service != nil {
		if d.service.cfg.Maintenance {
			return ErrStorageMaintenance
		}
		v, err = d.resolve(ctx, false)
	} else {
		v, err = d.resolve(ctx, true)
	}
	if err != nil {
		return err
	}
	return v.Delete(ctx, o)
}
func (d *storageGuardedDriver) Versions(ctx context.Context, key string) ([]storage.Object, error) {
	v, err := d.resolve(ctx, false)
	if err != nil {
		return nil, err
	}
	lister, ok := v.(storage.VersionLister)
	if !ok {
		return nil, storage.ErrUnsupported
	}
	return lister.Versions(ctx, key)
}
func (d *storageGuardedDriver) Capabilities(ctx context.Context) (storage.Capabilities, error) {
	v, err := d.resolve(ctx, false)
	if err != nil {
		return storage.Capabilities{}, err
	}
	inspector, ok := v.(storage.CapabilityInspector)
	if !ok {
		return storage.Capabilities{}, storage.ErrUnsupported
	}
	return inspector.Capabilities(ctx)
}
