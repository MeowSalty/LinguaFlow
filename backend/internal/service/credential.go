package service

import (
	"context"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	cred "github.com/MeowSalty/LinguaFlow/backend/internal/ent/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/credentialjobreference"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/credentialversion"
)

// CredentialService owns the process-wide lease/GC gate. Every API, resolver and
// worker in a process must use this same instance, including transactional jobs.
type CredentialService struct {
	client *ent.Client
	keys   *credential.Keyring
	users  *UserService
	mu     sync.Mutex
	leases map[credential.Binding]int
}

func NewCredentialService(client *ent.Client, keys *credential.Keyring, users *UserService) *CredentialService {
	if users == nil {
		users = NewUserService(client, nil)
	}
	return &CredentialService{client: client, keys: keys, users: users, leases: make(map[credential.Binding]int)}
}

type CreateCredentialInput struct {
	Scope    string `json:"scope"`
	OwnerID  int    `json:"owner_id"`
	Provider string `json:"provider"`
	Endpoint string `json:"endpoint"`
	Secret   string `json:"-"`
}
type CredentialRecord struct {
	ID             int    `json:"id"`
	Scope          string `json:"scope"`
	OwnerID        int    `json:"owner_id"`
	Provider       string `json:"provider"`
	Endpoint       string `json:"endpoint"`
	CurrentVersion int    `json:"current_version"`
}
type CredentialVersionRecord struct {
	Version   int       `json:"version"`
	Revoked   bool      `json:"revoked"`
	CreatedAt time.Time `json:"created_at"`
}

func credentialRecord(row *ent.Credential) *CredentialRecord {
	return &CredentialRecord{ID: row.ID, Scope: row.Scope, OwnerID: row.OwnerID, Provider: row.Provider, Endpoint: row.Endpoint, CurrentVersion: row.CurrentVersion}
}

func (s *CredentialService) requireOwner(ctx context.Context, actorID int, scope string, ownerID int) error {
	if scope == ScopeUser && ownerID == actorID {
		return nil
	}
	if scope == ScopeOrg {
		_, err := s.users.RequireMembership(ctx, actorID, ownerID, OrgRoleAdmin)
		return err
	}
	return ErrForbidden
}
func (s *CredentialService) authorized(ctx context.Context, actorID, id int) (*ent.Credential, error) {
	row, err := s.client.Credential.Get(ctx, id)
	if ent.IsNotFound(err) {
		return nil, credential.ErrUnavailable
	}
	if err != nil {
		return nil, err
	}
	if err := s.requireOwner(ctx, actorID, row.Scope, row.OwnerID); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *CredentialService) Create(ctx context.Context, actorID int, input CreateCredentialInput) (*CredentialRecord, error) {
	if err := s.requireOwner(ctx, actorID, input.Scope, input.OwnerID); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	row, err := s.createWith(ctx, tx.Client(), input)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return credentialRecord(row), nil
}

// createWith runs under mu and the caller's transaction, allowing Backend and
// its first secret to commit atomically.
func (s *CredentialService) createWith(ctx context.Context, client *ent.Client, input CreateCredentialInput) (*ent.Credential, error) {
	if input.OwnerID <= 0 || (input.Scope != ScopeUser && input.Scope != ScopeOrg) || !isAllowedBackendType(input.Provider) || input.Secret == "" {
		return nil, credential.ErrInvalid
	}
	ep, err := credential.NormalizeEndpoint(input.Provider, input.Endpoint)
	if err != nil {
		return nil, err
	}
	row, err := client.Credential.Create().SetScope(input.Scope).SetOwnerID(input.OwnerID).SetProvider(input.Provider).SetEndpoint(ep).SetCurrentVersion(1).Save(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.storeVersion(ctx, client, row, 1, input.Secret); err != nil {
		return nil, err
	}
	return row, nil
}
func credentialAAD(row *ent.Credential, version int) credential.AssociatedData {
	return credential.AssociatedData{ID: row.ID, Version: version, Provider: row.Provider, Endpoint: row.Endpoint, Scope: row.Scope, OwnerID: row.OwnerID}
}
func (s *CredentialService) storeVersion(ctx context.Context, client *ent.Client, row *ent.Credential, version int, secret string) (*ent.CredentialVersion, error) {
	encrypted, err := s.keys.Encrypt(secret, credentialAAD(row, version))
	if err != nil {
		return nil, err
	}
	return client.CredentialVersion.Create().SetCredentialID(row.ID).SetVersion(version).SetEncryptionVersion(encrypted.Version).SetKeyID(encrypted.KeyID).SetNonce(encrypted.Nonce).SetCiphertext(encrypted.Data).Save(ctx)
}

func (s *CredentialService) List(ctx context.Context, actorID int, scope string, ownerID int) ([]*CredentialRecord, error) {
	if err := s.requireOwner(ctx, actorID, scope, ownerID); err != nil {
		return nil, err
	}
	rows, err := s.client.Credential.Query().Where(cred.ScopeEQ(scope), cred.OwnerIDEQ(ownerID)).Order(ent.Asc(cred.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*CredentialRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, credentialRecord(row))
	}
	return out, nil
}
func (s *CredentialService) Versions(ctx context.Context, actorID, id int) ([]CredentialVersionRecord, error) {
	if _, err := s.authorized(ctx, actorID, id); err != nil {
		return nil, err
	}
	rows, err := s.client.CredentialVersion.Query().Where(credentialversion.CredentialIDEQ(id)).Order(ent.Asc(credentialversion.FieldVersion)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]CredentialVersionRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, CredentialVersionRecord{Version: r.Version, Revoked: r.Revoked, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

func (s *CredentialService) Rotate(ctx context.Context, actorID, id int, secret string) (credential.Binding, error) {
	if secret == "" {
		return credential.Binding{}, credential.ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.authorized(ctx, actorID, id)
	if err != nil {
		return credential.Binding{}, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return credential.Binding{}, err
	}
	defer tx.Rollback()
	version := row.CurrentVersion + 1
	if _, err := s.storeVersion(ctx, tx.Client(), row, version, secret); err != nil {
		return credential.Binding{}, err
	}
	if _, err := tx.Credential.UpdateOneID(id).SetCurrentVersion(version).Save(ctx); err != nil {
		return credential.Binding{}, err
	}
	if err := tx.Commit(); err != nil {
		return credential.Binding{}, err
	}
	return credential.Binding{ID: id, Version: version}, nil
}
func (s *CredentialService) Revoke(ctx context.Context, actorID int, b credential.Binding) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.authorized(ctx, actorID, b.ID); err != nil {
		return err
	}
	v, err := s.version(ctx, s.client, b)
	if err != nil {
		return err
	}
	return s.client.CredentialVersion.UpdateOneID(v.ID).SetRevoked(true).Exec(ctx)
}

func (s *CredentialService) version(ctx context.Context, client *ent.Client, b credential.Binding) (*ent.CredentialVersion, error) {
	if !b.Valid() {
		return nil, credential.ErrUnavailable
	}
	v, err := client.CredentialVersion.Query().Where(credentialversion.CredentialIDEQ(b.ID), credentialversion.VersionEQ(b.Version)).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, credential.ErrUnavailable
	}
	return v, err
}
func (s *CredentialService) checkWith(ctx context.Context, client *ent.Client, b credential.Binding, backendID int, provider, endpoint string) (*ent.Credential, *ent.CredentialVersion, error) {
	if backendID > 0 {
		if _, err := client.Backend.Get(ctx, backendID); err != nil {
			if ent.IsNotFound(err) {
				return nil, nil, credential.ErrBackendDeleted
			}
			return nil, nil, err
		}
	}
	row, err := client.Credential.Get(ctx, b.ID)
	if ent.IsNotFound(err) {
		return nil, nil, credential.ErrUnavailable
	}
	if err != nil {
		return nil, nil, err
	}
	ep, err := credential.NormalizeEndpoint(provider, endpoint)
	if err != nil {
		return nil, nil, err
	}
	if row.Provider != provider || row.Endpoint != ep {
		return nil, nil, credential.ErrEndpoint
	}
	v, err := s.version(ctx, client, b)
	if err != nil {
		return nil, nil, err
	}
	if v.Revoked {
		return nil, nil, credential.ErrRevoked
	}
	return row, v, nil
}
func (s *CredentialService) Check(ctx context.Context, b credential.Binding, backendID int, provider, endpoint string) error {
	_, _, err := s.checkWith(ctx, s.client, b, backendID, provider, endpoint)
	return err
}
func (s *CredentialService) Resolve(ctx context.Context, b credential.Binding, provider, endpoint string) (string, error) {
	row, v, err := s.checkWith(ctx, s.client, b, 0, provider, endpoint)
	if err != nil {
		return "", err
	}
	return s.keys.Decrypt(credential.Ciphertext{Version: v.EncryptionVersion, KeyID: v.KeyID, Nonce: v.Nonce, Data: v.Ciphertext}, credentialAAD(row, b.Version))
}

func (s *CredentialService) leaseLocked(b credential.Binding) func() {
	s.leases[b]++
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.leases[b]--
			if s.leases[b] == 0 {
				delete(s.leases, b)
			}
		})
	}
}
func (s *CredentialService) Acquire(ctx context.Context, b credential.Binding) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, err := s.version(ctx, s.client, b)
	if err != nil {
		return nil, err
	}
	if v.Revoked {
		return nil, credential.ErrRevoked
	}
	return s.leaseLocked(b), nil
}
func (s *CredentialService) AcquireBackend(ctx context.Context, backendID int) (credential.Binding, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := s.client.Backend.Get(ctx, backendID)
	if ent.IsNotFound(err) {
		return credential.Binding{}, nil, credential.ErrBackendDeleted
	}
	if err != nil {
		return credential.Binding{}, nil, err
	}
	if b.CredentialID == nil {
		return credential.Binding{}, nil, credential.ErrUnavailable
	}
	row, err := s.client.Credential.Get(ctx, *b.CredentialID)
	if err != nil {
		return credential.Binding{}, nil, err
	}
	binding := credential.Binding{ID: row.ID, Version: row.CurrentVersion}
	v, err := s.version(ctx, s.client, binding)
	if err != nil {
		return credential.Binding{}, nil, err
	}
	if v.Revoked {
		return credential.Binding{}, nil, credential.ErrRevoked
	}
	return binding, s.leaseLocked(binding), nil
}

// RetainJob must be called in the Job creation transaction. The caller keeps
// acquired leases until that transaction commits or rolls back. The FK prevents
// any collector from deleting a version once ownership transfers to the Job.
func (s *CredentialService) RetainJob(ctx context.Context, tx *ent.Tx, jobID int, bindings []credential.Binding) error {
	seen := map[credential.Binding]bool{}
	for _, b := range bindings {
		if seen[b] {
			continue
		}
		seen[b] = true
		v, err := s.version(ctx, tx.Client(), b)
		if err != nil {
			return err
		}
		if v.Revoked {
			return credential.ErrRevoked
		}
		if _, err := tx.CredentialJobReference.Create().SetJobID(jobID).SetCredentialVersionID(v.ID).Save(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Collect is intentionally an instance method for the running service API.
// Current versions and all existing Job references are retained conservatively.
func (s *CredentialService) Collect(ctx context.Context, actorID, id int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.authorized(ctx, actorID, id)
	if err != nil {
		return 0, err
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	versions, err := tx.CredentialVersion.Query().Where(credentialversion.CredentialIDEQ(id), credentialversion.VersionNEQ(row.CurrentVersion)).All(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, v := range versions {
		if s.leases[credential.Binding{ID: id, Version: v.Version}] > 0 {
			continue
		}
		used, err := tx.CredentialJobReference.Query().Where(credentialjobreference.CredentialVersionIDEQ(v.ID)).Exist(ctx)
		if err != nil {
			return 0, err
		}
		if used {
			continue
		}
		if err := tx.CredentialVersion.DeleteOneID(v.ID).Exec(ctx); err != nil {
			return 0, err
		}
		count++
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// ValidateKeys authenticates every stored version before startup. A keyring with
// the right IDs but incorrect key bytes must not make an instance ready.
func (s *CredentialService) ValidateKeys(ctx context.Context) error {
	versions, err := s.client.CredentialVersion.Query().WithCredential().All(ctx)
	if err != nil {
		return err
	}
	for _, version := range versions {
		if !s.keys.HasKey(version.KeyID) {
			return credential.ErrKeyUnavailable
		}
		if version.Edges.Credential == nil {
			return credential.ErrUnavailable
		}
		_, err := s.keys.Decrypt(credential.Ciphertext{Version: version.EncryptionVersion, KeyID: version.KeyID, Nonce: version.Nonce, Data: version.Ciphertext}, credentialAAD(version.Edges.Credential, version.Version))
		if err != nil {
			return err
		}
	}
	return nil
}

// Reencrypt rewrites one row per transaction and is safe to resume. Operators
// first deploy the new active key and restart all writers. This method never
// changes logical credential versions, revocation, or Job bindings.
func (s *CredentialService) Reencrypt(ctx context.Context) (int, error) {
	if s.keys == nil {
		return 0, credential.ErrKeyUnavailable
	}
	count := 0
	for {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		s.mu.Lock()
		changed, err := s.reencryptOne(ctx)
		s.mu.Unlock()
		if err != nil {
			return count, err
		}
		if !changed {
			return count, nil
		}
		count++
	}
}
func (s *CredentialService) reencryptOne(ctx context.Context) (bool, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	v, err := tx.CredentialVersion.Query().Where(credentialversion.KeyIDNEQ(s.keys.ActiveKeyID())).Order(ent.Asc(credentialversion.FieldID)).First(ctx)
	if ent.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	row, err := tx.Credential.Get(ctx, v.CredentialID)
	if err != nil {
		return false, err
	}
	aad := credentialAAD(row, v.Version)
	secret, err := s.keys.Decrypt(credential.Ciphertext{Version: v.EncryptionVersion, KeyID: v.KeyID, Nonce: v.Nonce, Data: v.Ciphertext}, aad)
	if err != nil {
		return false, err
	}
	encrypted, err := s.keys.Encrypt(secret, aad)
	if err != nil {
		return false, err
	}
	if err := tx.CredentialVersion.UpdateOneID(v.ID).SetEncryptionVersion(encrypted.Version).SetKeyID(encrypted.KeyID).SetNonce(encrypted.Nonce).SetCiphertext(encrypted.Data).Exec(ctx); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

var _ credential.Reader = (*CredentialService)(nil)
var _ credential.Checker = (*CredentialService)(nil)
