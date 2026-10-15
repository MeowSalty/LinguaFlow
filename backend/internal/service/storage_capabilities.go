package service

import (
	"context"
	"errors"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageauthversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
)

type StorageManagementActions struct {
	CreateConnection StorageActionAvailability `json:"create_connection"`
}

type StorageCapabilities struct {
	Scope             string                   `json:"scope"`
	OwnerID           int                      `json:"owner_id"`
	Runtime           StorageRuntime           `json:"runtime"`
	ManagementActions StorageManagementActions `json:"management_actions"`
}

type StorageConnectionManagementActions struct {
	Retire         StorageActionAvailability `json:"retire"`
	CreateSpace    StorageActionAvailability `json:"create_space"`
	AuthorizeRead  StorageActionAvailability `json:"authorize_read"`
	AuthorizeWrite StorageActionAvailability `json:"authorize_write"`
	CheckRead      StorageActionAvailability `json:"check_read"`
	CheckWrite     StorageActionAvailability `json:"check_write"`
	RevokeAuth     StorageActionAvailability `json:"revoke_auth"`
	SetStatus      StorageActionAvailability `json:"set_status"`
}

type StorageSpaceManagementActions struct {
	Retire    StorageActionAvailability `json:"retire"`
	SetQuota  StorageActionAvailability `json:"set_quota"`
	SetStatus StorageActionAvailability `json:"set_status"`
}

// Capabilities also works for owners with no connections. No provider is queried.
func (s *StorageConnectionService) Capabilities(ctx context.Context, actor int, scope string, organizationID *int) (StorageCapabilities, error) {
	owner := actor
	switch scope {
	case ScopeUser:
		if organizationID != nil {
			return StorageCapabilities{}, ErrInvalidInput
		}
	case ScopeOrg:
		if organizationID == nil || *organizationID <= 0 {
			return StorageCapabilities{}, ErrInvalidInput
		}
		owner = *organizationID
	default:
		return StorageCapabilities{}, ErrInvalidInput
	}
	if err := s.requireOwner(ctx, s.client, actor, scope, owner); err != nil {
		return StorageCapabilities{}, err
	}
	runtime := s.Runtime()
	return StorageCapabilities{Scope: scope, OwnerID: owner, Runtime: runtime, ManagementActions: StorageManagementActions{
		CreateConnection: storageAvailability(storageOperationReasons(runtime, storageOpCreateConnection, nil)),
	}}, nil
}

// These facts are shared by response projection and management admission. The
// caller must first establish ownership; the queries only read local metadata.
type storageConnectionManagementFacts struct {
	spaces []*ent.StorageSpace
	auth   *ent.StorageAuthVersion
	sealed bool
}

func (s *StorageConnectionService) managementFacts(ctx context.Context, client *ent.Client, c *ent.StorageConnection) (storageConnectionManagementFacts, error) {
	facts := storageConnectionManagementFacts{}
	var err error
	facts.spaces, err = client.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(c.ID), storagespace.LifecycleNEQ(storagespace.LifecycleRemoved)).Order(ent.Asc(storagespace.FieldID)).Limit(101).All(ctx)
	if err != nil {
		return facts, err
	}
	ids := []*int{c.RetirementID}
	for _, sp := range facts.spaces {
		ids = append(ids, sp.RetirementID)
	}
	for _, id := range ids {
		if id != nil {
			r, e := client.StorageRetirement.Get(ctx, *id)
			if e != nil {
				return facts, e
			}
			if r.Phase == "finalizing" || r.Phase == "waiting_configuration" {
				facts.sealed = true
			}
		}
	}
	if c.AuthSource == storageconnection.AuthSourceStored && c.ActiveAuthGeneration > 0 {
		facts.auth, err = client.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(c.ID), storageauthversion.GenerationEQ(c.ActiveAuthGeneration), storageauthversion.StatusEQ(storageauthversion.StatusActive), storageauthversion.PurposeEQ(storageauthversion.PurposeBusiness), storageauthversion.RetirementIDIsNil(), storageauthversion.PurgedAtIsNil()).Only(ctx)
		if ent.IsNotFound(err) {
			err = nil
		}
	}
	return facts, err
}

func (s *StorageConnectionService) managementReasons(c *ent.StorageConnection, facts storageConnectionManagementFacts, op storageOperation) []string {
	reasons := storageOperationReasons(s.Runtime(), op, c)
	if c.Lifecycle == storageconnection.LifecycleRemoved {
		return normalizeStorageReasons(append(reasons, "storage_removed"))
	}
	newUse := op == storageOpCreateSpace || op == storageOpAuthorizeWrite || op == storageOpCheckWrite
	if c.Lifecycle == storageconnection.LifecycleRetiring && newUse {
		reasons = append(reasons, "storage_retiring")
	}
	if facts.sealed && op != storageOpRevokeAuth && op != storageOpSetStatus && op != storageOpCreateSpace {
		reasons = append(reasons, "storage_retiring")
	}
	for _, sp := range facts.spaces {
		if newUse && op != storageOpCreateSpace && sp.Lifecycle == storagespace.LifecycleRetiring {
			reasons = appendReason(reasons, "storage_retiring")
		}
	}
	stored := c.AuthSource == storageconnection.AuthSourceStored && c.OwnerKind != storageconnection.OwnerKindSite && c.Driver == storageconnection.DriverS3
	switch op {
	case storageOpCreateSpace, storageOpAuthorizeRead, storageOpAuthorizeWrite, storageOpRevokeAuth, storageOpSetStatus:
		if !stored {
			reasons = append(reasons, "storage_capability_unsupported")
			return normalizeStorageReasons(reasons)
		}
	case storageOpCheckRead, storageOpCheckWrite:
		if c.Driver != storageconnection.DriverS3 {
			reasons = append(reasons, "storage_capability_unsupported")
			return normalizeStorageReasons(reasons)
		}
	}
	if op == storageOpRevokeAuth || op == storageOpSetStatus {
		return normalizeStorageReasons(reasons)
	}
	if c.Status != storageconnection.StatusEnabled {
		reasons = append(reasons, "connection_disabled")
	}
	if op == storageOpCreateSpace {
		return normalizeStorageReasons(reasons)
	}
	if len(facts.spaces) == 0 {
		reasons = append(reasons, "storage_space_required")
	} else if len(facts.spaces) > 100 {
		reasons = append(reasons, "storage_check_space_limit_exceeded")
	}
	write := op == storageOpAuthorizeWrite || op == storageOpCheckWrite
	usable := 0
	for _, sp := range facts.spaces {
		if sp.Status == storagespace.StatusDisabled {
			continue
		}
		usable++
		if write && sp.Status != storagespace.StatusActive {
			reasons = append(reasons, "space_read_only")
		}
		// A write check must at least be able to reserve its bounded probe.
		// Marker creation, when needed, performs the exact byte check at execution.
		available, quotaErr := storageAvailableBytes(sp)
		if write && (quotaErr != nil || (available != nil && *available < storageProbePayloadBytes)) {
			reasons = append(reasons, "storage_quota_exceeded")
		}
	}
	if len(facts.spaces) > 0 && usable == 0 {
		reasons = append(reasons, "space_disabled")
	}
	if op == storageOpAuthorizeRead || op == storageOpAuthorizeWrite {
		// Updating authorization must remain possible when its predecessor is
		// missing, revoked or expired. Only the encryption key for the new one matters.
		if !s.keys.HasKey(s.keys.ActiveKeyID()) {
			reasons = append(reasons, "storage_crypto_unavailable")
		}
	} else if c.AuthSource == storageconnection.AuthSourceStored {
		a := facts.auth
		if a == nil || a.ExpiresAt != nil && !a.ExpiresAt.After(time.Now()) {
			reasons = append(reasons, "storage_auth_required")
		} else if !s.keys.HasKey(a.KeyID) || a.AadVersion != storageauth.AADVersion || a.PayloadVersion != storageauth.PayloadVersion {
			reasons = append(reasons, "storage_crypto_unavailable")
		}
	} else if c.AuthSource == storageconnection.AuthSourceDeployment {
		for _, sp := range facts.spaces {
			if sp.Status == storagespace.StatusDisabled {
				continue
			}
			if _, err := s.deploymentCredentials(c, sp); err != nil {
				if errors.Is(err, storage.ErrAuthRequired) {
					reasons = append(reasons, "storage_auth_required")
				} else {
					reasons = append(reasons, "storage_unavailable")
				}
			}
		}
	}
	if c.Driver == storageconnection.DriverS3 && s.factory == nil {
		reasons = append(reasons, "storage_unavailable")
	}
	return normalizeStorageReasons(reasons)
}

func (s *StorageConnectionService) admitManagement(ctx context.Context, client *ent.Client, c *ent.StorageConnection, op storageOperation) error {
	facts := storageConnectionManagementFacts{}
	if op != storageOpCreateSpace && op != storageOpRevokeAuth && op != storageOpSetStatus {
		var err error
		facts, err = s.managementFacts(ctx, client, c)
		if err != nil {
			return err
		}
	}
	return storageAdmissionError(s.managementReasons(c, facts, op))
}

func (s *StorageConnectionService) connectionRecord(ctx context.Context, client *ent.Client, c *ent.StorageConnection) (*StorageConnectionRecord, error) {
	facts, err := s.managementFacts(ctx, client, c)
	if err != nil {
		return nil, err
	}
	action := func(op storageOperation) StorageActionAvailability {
		return storageAvailability(s.managementReasons(c, facts, op))
	}
	out := storageConnectionRecord(c)
	out.ManagementActions = StorageConnectionManagementActions{
		Retire:      s.retireAvailability(c, nil),
		CreateSpace: action(storageOpCreateSpace), AuthorizeRead: action(storageOpAuthorizeRead), AuthorizeWrite: action(storageOpAuthorizeWrite),
		CheckRead: action(storageOpCheckRead), CheckWrite: action(storageOpCheckWrite), RevokeAuth: action(storageOpRevokeAuth), SetStatus: action(storageOpSetStatus),
	}
	return out, nil
}

func (s *StorageConnectionService) spaceRecord(ctx context.Context, sp *ent.StorageSpace) (*StorageSpaceRecord, error) {
	available, err := storageAvailableBytes(sp)
	if err != nil {
		return nil, err
	}
	c, err := s.client.StorageConnection.Get(ctx, sp.ConnectionID)
	if err != nil {
		return nil, err
	}
	out := storageSpaceRecord(sp, c)
	out.ManagementActions.Retire = s.retireAvailability(c, sp)
	out.AvailableBytes = available
	out.ManagementActions.SetQuota = storageAvailability(nil)
	out.ManagementActions.SetStatus = storageAvailability(storageOperationReasons(s.Runtime(), storageOpSetStatus, nil))
	return out, nil
}

func (s *StorageConnectionService) retireAvailability(c *ent.StorageConnection, sp *ent.StorageSpace) StorageActionAvailability {
	if s.retirements == nil || s.retirements.local || c.Driver != storageconnection.DriverS3 {
		return storageRetirementAvailability()
	}
	reasons := []string{}
	if s.cfg.Maintenance {
		reasons = append(reasons, "storage_maintenance")
	}
	if c.Lifecycle == storageconnection.LifecycleRemoved || (sp != nil && sp.Lifecycle == storagespace.LifecycleRemoved) {
		reasons = append(reasons, "storage_removed")
	} else if c.Lifecycle == storageconnection.LifecycleRetiring || (sp != nil && sp.Lifecycle == storagespace.LifecycleRetiring) {
		reasons = append(reasons, "storage_retirement_conflict")
	}
	return storageAvailability(reasons)
}

func (s *StorageConnectionService) driverAdmission(c *ent.StorageConnection, sp *ent.StorageSpace, write bool) error {
	op := storageOpRead
	if write {
		op = storageOpWrite
	}
	reasons := storageOperationReasons(s.Runtime(), op, c)
	if c.Lifecycle == storageconnection.LifecycleRemoved || sp.Lifecycle == storagespace.LifecycleRemoved {
		reasons = append(reasons, "storage_removed")
	} else if write && (c.Lifecycle == storageconnection.LifecycleRetiring || sp.Lifecycle == storagespace.LifecycleRetiring) {
		reasons = append(reasons, "storage_retiring")
	}
	if c.Status != storageconnection.StatusEnabled {
		reasons = append(reasons, "connection_disabled")
	}
	if sp.Status == storagespace.StatusDisabled {
		reasons = append(reasons, "space_disabled")
	} else if write && sp.Status != storagespace.StatusActive {
		reasons = append(reasons, "space_read_only")
	}
	return storageAdmissionError(reasons)
}
