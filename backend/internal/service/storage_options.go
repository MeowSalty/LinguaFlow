package service

import (
	"context"
	"math"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sourcerevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageauthversion"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagemigrationitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/user"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

// StorageOptions contains only information needed to choose a target. Connection
// identities, credentials and the space-wide capacity ledger are management data.
type StorageOptions struct {
	Scope                    string               `json:"scope"`
	OwnerID                  int                  `json:"owner_id"`
	Policy                   StorageOptionsPolicy `json:"policy"`
	DefaultSpaceID           *int                 `json:"default_space_id"`
	DefaultUnavailableReason *string              `json:"default_unavailable_reason"`
	Items                    []StorageOption      `json:"items"`
	StorageGeneration        *int64               `json:"storage_generation,omitempty"`
}

type StorageOptionsPolicy struct {
	Mode          string `json:"mode"`
	DefaultChoice string `json:"default_choice"`
	Generation    int64  `json:"generation"`
}

type StorageOption struct {
	SpaceID     int      `json:"space_id"`
	Name        string   `json:"name"`
	Scope       string   `json:"scope"`
	Selectable  bool     `json:"selectable"`
	ReasonCodes []string `json:"reason_codes"`
}

type ProjectStorageRecord struct {
	ProjectID         int                    `json:"project_id"`
	StorageGeneration int64                  `json:"storage_generation"`
	StorageState      string                 `json:"storage_state"`
	MigrationTaskID   *int                   `json:"migration_task_id"`
	Binding           *ProjectStorageBinding `json:"binding"`
	ReasonCodes       []string               `json:"reason_codes"`
}

type ProjectStorageBinding struct {
	SpaceID    int    `json:"space_id"`
	Name       string `json:"name"`
	Scope      string `json:"scope"`
	OwnerID    int    `json:"owner_id"`
	Status     string `json:"status"`
	Historical bool   `json:"historical"`
}

// Options discovers creation targets. Personal ownership can never be supplied
// by the caller; organization creation requires the same role as CreateOrgProject.
func (s *StorageService) Options(ctx context.Context, actor int, scope string, organizationID int) (*StorageOptions, error) {
	active, err := s.client.User.Query().Where(user.IDEQ(actor), user.ActiveEQ(true)).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, ErrForbidden
	}
	p := &ent.Project{StorageState: "active"}
	switch scope {
	case ScopeUser:
		if organizationID != 0 {
			return nil, ErrInvalidInput
		}
		p.OwnerUserID = &actor
	case ScopeOrg:
		if organizationID <= 0 {
			return nil, ErrInvalidInput
		}
		if _, err := requireOrganizationMembership(ctx, s.client, actor, organizationID, OrgRoleAdmin); err != nil {
			return nil, err
		}
		p.OwnerOrgID = &organizationID
	default:
		return nil, ErrInvalidInput
	}
	return s.storageOptions(ctx, p, "create", 0)
}

func (s *StorageService) ProjectOptions(ctx context.Context, actor, projectID int, purpose string, sourceRevisionID int) (*StorageOptions, error) {
	if purpose != "bind" && purpose != "migrate" && purpose != "repair" {
		return nil, ErrInvalidInput
	}
	if (purpose == "repair" && sourceRevisionID <= 0) || (purpose != "repair" && sourceRevisionID != 0) {
		return nil, ErrInvalidInput
	}
	p, err := s.projects.requireProjectAccess(ctx, actor, projectID, true)
	if err != nil {
		return nil, err
	}
	return s.storageOptions(ctx, p, purpose, sourceRevisionID)
}

func (s *StorageService) storageOptions(ctx context.Context, p *ent.Project, purpose string, revisionID int) (*StorageOptions, error) {
	policy, err := storagePolicy(ctx, s.client)
	if err != nil {
		return nil, err
	}
	kind, owner := storageProjectOwner(p)
	rows, err := s.client.StorageSpace.Query().Where(storagespace.Or(
		storagespace.OwnerKindEQ(storagespace.OwnerKindSite),
		storagespace.And(storagespace.OwnerKindEQ(storagespace.OwnerKind(kind)), storagespace.OwnerIDEQ(owner)),
	)).Order(ent.Asc(storagespace.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	out := &StorageOptions{Scope: kind, OwnerID: owner,
		Policy: StorageOptionsPolicy{Mode: policy.Mode, DefaultChoice: policy.DefaultChoice, Generation: policy.Generation},
		Items:  make([]StorageOption, 0, len(rows))}
	if p.ID != 0 {
		out.StorageGeneration = &p.StorageGeneration
	}
	currentSpace, size := 0, int64(-1)
	if purpose == "repair" {
		currentSpace, size, err = storageRepairLocation(ctx, s.client, p.ID, revisionID)
		if err != nil {
			return nil, err
		}
	} else if purpose == "migrate" {
		size, err = storageMigrationSize(ctx, s.client, p.ID)
		if err != nil {
			return nil, err
		}
	}
	var operationReason string
	if purpose == "bind" {
		operationReason, err = storageRebindReason(ctx, s.client, p.ID)
		if err != nil {
			return nil, err
		}
	}
	for _, sp := range rows {
		reasons, err := s.storageTargetReasons(ctx, s.client, p, sp, policy, size, purpose == "repair" && sp.ID == currentSpace)
		if err != nil {
			return nil, err
		}
		if operationReason != "" {
			reasons = append(reasons, operationReason)
		}
		if p.StorageState != "active" {
			allowed := false
			if purpose == "repair" {
				allowed, err = storageMigrationRepairTarget(ctx, s.client, p, revisionID, sp.ID)
				if err != nil {
					return nil, err
				}
			}
			if !allowed {
				reasons = appendReason(reasons, "storage_maintenance")
			}
		}
		out.Items = append(out.Items, StorageOption{SpaceID: sp.ID, Name: sp.Name, Scope: string(sp.OwnerKind), Selectable: len(reasons) == 0, ReasonCodes: reasons})
	}
	if policy.DefaultChoice == "user" {
		reason := "selection_required"
		out.DefaultUnavailableReason = &reason
		return out, nil
	}
	for _, item := range out.Items {
		if item.SpaceID != s.defaultSpaceID {
			continue
		}
		if item.Selectable {
			id := item.SpaceID
			out.DefaultSpaceID = &id
		} else {
			reason := item.ReasonCodes[0]
			out.DefaultUnavailableReason = &reason
		}
		return out, nil
	}
	reason := "selection_required"
	out.DefaultUnavailableReason = &reason
	return out, nil
}

func (s *StorageService) ProjectStorage(ctx context.Context, actor, projectID int) (*ProjectStorageRecord, error) {
	p, err := s.projects.requireProjectAccess(ctx, actor, projectID, false)
	if err != nil {
		return nil, err
	}
	out := &ProjectStorageRecord{ProjectID: p.ID, StorageGeneration: p.StorageGeneration, StorageState: p.StorageState,
		MigrationTaskID: p.StorageMigrationTaskID, ReasonCodes: []string{}}
	if p.StorageState != "active" || s.maintenance {
		out.ReasonCodes = append(out.ReasonCodes, "storage_maintenance")
	}
	if p.StorageSpaceID == nil {
		out.ReasonCodes = append(out.ReasonCodes, "selection_required")
		return out, nil
	}
	sp, err := s.client.StorageSpace.Get(ctx, *p.StorageSpaceID)
	if err != nil {
		return nil, err
	}
	policy, err := storagePolicy(ctx, s.client)
	if err != nil {
		return nil, err
	}
	out.Binding = &ProjectStorageBinding{SpaceID: sp.ID, Name: sp.Name, Scope: string(sp.OwnerKind), OwnerID: sp.OwnerID,
		Status: string(sp.Status), Historical: !storagePolicyAllows(policy, p, sp)}
	reasons, err := s.storageTargetReasons(ctx, s.client, p, sp, policy, -1, true)
	if err != nil {
		return nil, err
	}
	for _, reason := range reasons {
		out.ReasonCodes = appendReason(out.ReasonCodes, reason)
	}
	return out, nil
}

func storagePolicyAllows(policy StoragePolicy, p *ent.Project, sp *ent.StorageSpace) bool {
	if sp.OwnerKind == storagespace.OwnerKindSite {
		return policy.Mode != "user_required"
	}
	kind, owner := storageProjectOwner(p)
	return policy.Mode != "site_only" && string(sp.OwnerKind) == kind && sp.OwnerID == owner
}

func appendReason(reasons []string, code string) []string {
	for _, reason := range reasons {
		if reason == code {
			return reasons
		}
	}
	return append(reasons, code)
}

// storageTargetReasons is deliberately metadata-only: discovery never probes a
// provider. Registration and publication use the same rules and still perform
// their own atomic reservations and generation checks.
func (s *StorageService) storageTargetReasons(ctx context.Context, client *ent.Client, p *ent.Project, sp *ent.StorageSpace, policy StoragePolicy, size int64, existing bool) ([]string, error) {
	reasons := []string{}
	kind, owner := storageProjectOwner(p)
	owned := sp.OwnerKind == storagespace.OwnerKindSite || string(sp.OwnerKind) == kind && sp.OwnerID == owner
	if !owned || (!existing && !storagePolicyAllows(policy, p, sp)) {
		reasons = append(reasons, "policy_disallowed")
	}
	if sp.OwnerKind != storagespace.OwnerKindSite && !s.cfg.Enabled {
		reasons = append(reasons, "byos_disabled")
	}
	if s.maintenance {
		reasons = append(reasons, "storage_maintenance")
	}
	conn, err := client.StorageConnection.Get(ctx, sp.ConnectionID)
	if err != nil {
		return nil, err
	}
	if string(conn.OwnerKind) != string(sp.OwnerKind) || conn.OwnerID != sp.OwnerID {
		reasons = appendReason(reasons, "policy_disallowed")
	}
	if conn.Status != storageconnection.StatusEnabled {
		reasons = append(reasons, "connection_disabled")
	}
	if conn.AuthSource == storageconnection.AuthSourceStored {
		auth, err := client.StorageAuthVersion.Query().Where(storageauthversion.ConnectionIDEQ(conn.ID),
			storageauthversion.GenerationEQ(conn.ActiveAuthGeneration), storageauthversion.StatusEQ(storageauthversion.StatusActive)).Only(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return nil, err
		}
		if auth == nil || conn.ActiveAuthGeneration == 0 || (auth.ExpiresAt != nil && !auth.ExpiresAt.After(time.Now())) {
			reasons = append(reasons, "storage_auth_required")
		}
	}
	switch conn.Health {
	case "crypto_unavailable":
		reasons = append(reasons, "storage_crypto_unavailable")
	case "auth_required":
		reasons = appendReason(reasons, "storage_auth_required")
	case "permission_denied":
		reasons = append(reasons, "storage_permission_denied")
	case "", "unknown", "available", "degraded":
	default:
		reasons = append(reasons, "storage_unavailable")
	}
	if !sp.Verified {
		reasons = append(reasons, "space_unverified")
	}
	switch sp.Status {
	case storagespace.StatusReadOnly:
		reasons = append(reasons, "space_read_only")
	case storagespace.StatusActive:
	default:
		reasons = append(reasons, "space_disabled")
	}
	// Saturating subtraction avoids overflow and retains over-quota accounting.
	available := sp.CapacityBytes
	for _, used := range []int64{sp.ReservedBytes, sp.CandidateBytes, sp.LiveBytes, sp.PendingDeleteBytes} {
		if used >= available {
			available = 0
			break
		}
		available -= used
	}
	if (size < 0 && available == 0) || size > available {
		reasons = append(reasons, "storage_quota_exceeded")
	}
	return reasons, nil
}

func (s *StorageService) validateStorageTarget(ctx context.Context, client *ent.Client, p *ent.Project, id int, size int64, existing bool) error {
	sp, err := client.StorageSpace.Get(ctx, id)
	if ent.IsNotFound(err) {
		return ErrStoragePolicy
	}
	if err != nil {
		return err
	}
	policy, err := storagePolicy(ctx, client)
	if err != nil {
		return err
	}
	reasons, err := s.storageTargetReasons(ctx, client, p, sp, policy, size, existing)
	if err != nil || len(reasons) == 0 {
		return err
	}
	switch reasons[0] {
	case "policy_disallowed", "byos_disabled":
		return ErrStoragePolicy
	case "storage_maintenance", "space_read_only":
		return ErrStorageMaintenance
	case "storage_auth_required", "space_unverified":
		return storage.ErrAuthRequired
	case "storage_crypto_unavailable":
		return ErrStorageCrypto
	case "connection_disabled", "space_disabled", "storage_permission_denied":
		return storage.ErrPermission
	case "storage_quota_exceeded":
		return storage.ErrLimit
	default:
		return storage.ErrUnavailable
	}
}

// allowedExistingTarget preserves an existing binding across policy changes.
// All ownership, deployment, health and writable-space checks still apply.
func (s *StorageService) allowedExistingTarget(ctx context.Context, client *ent.Client, p *ent.Project, id int, size int64) error {
	return s.validateStorageTarget(ctx, client, p, id, size, true)
}

func (s *StorageService) allowedRepairTarget(ctx context.Context, client *ent.Client, p *ent.Project, revisionID, target int, size int64) error {
	current, _, err := storageRepairLocation(ctx, client, p.ID, revisionID)
	if err != nil {
		return err
	}
	if p.StorageState != "active" {
		allowed, err := storageMigrationRepairTarget(ctx, client, p, revisionID, target)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrStorageMaintenance
		}
	}
	return s.validateStorageTarget(ctx, client, p, target, size, current == target)
}

func storageRepairLocation(ctx context.Context, client *ent.Client, projectID, revisionID int) (int, int64, error) {
	revision, err := client.SourceRevision.Query().Where(sourcerevision.IDEQ(revisionID), sourcerevision.ProjectIDEQ(projectID), sourcerevision.DeletedEQ(false)).Only(ctx)
	if err != nil {
		return 0, 0, err
	}
	if revision.VerificationState != sourcerevision.VerificationStateVerified || revision.Size == nil || revision.Sha256 == nil {
		return 0, 0, ErrRepairMismatch
	}
	object, err := client.Blob.Get(ctx, revision.SourceBlobID)
	if err != nil {
		return 0, 0, err
	}
	if object.ProjectID != projectID || object.Size == nil || object.Sha256 == nil || *object.Size != *revision.Size || *object.Sha256 != *revision.Sha256 {
		return 0, 0, ErrRepairMismatch
	}
	size := int64(-1)
	if object.Size != nil {
		size = *object.Size
	}
	if object.ActiveLocationID == nil {
		return 0, size, nil
	}
	location, err := client.BlobLocation.Get(ctx, *object.ActiveLocationID)
	if err != nil {
		return 0, 0, err
	}
	return location.SpaceID, size, nil
}

// storageMigrationSize matches the coordinator's retained-Blob manifest. Unknown
// legacy identities are not converted into a fictitious zero-byte admission.
func storageMigrationSize(ctx context.Context, client *ent.Client, projectID int) (int64, error) {
	objects, err := client.Blob.Query().Where(blob.ProjectIDEQ(projectID), blob.StatusEQ(blob.StatusReady)).All(ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, object := range objects {
		if object.Size == nil {
			return -1, nil
		}
		if *object.Size > math.MaxInt64-total {
			return 0, storage.ErrLimit
		}
		total += *object.Size
	}
	return total, nil
}

func storageMigrationRepairTarget(ctx context.Context, client *ent.Client, p *ent.Project, revisionID, target int) (bool, error) {
	if (p.StorageState != "draining" && p.StorageState != "migrating") || p.StorageMigrationTaskID == nil {
		return false, nil
	}
	task, err := client.StorageTask.Get(ctx, *p.StorageMigrationTaskID)
	if err != nil {
		return false, err
	}
	if task.Kind != "migration" || task.ProjectID != p.ID || task.TargetSpaceID == nil || *task.TargetSpaceID != target || task.Status == storagetask.StatusCancelled || task.Phase == "committed" {
		return false, nil
	}
	// The coordinator freezes the manifest when entering draining. Repairs are
	// limited to its retained objects throughout the entire maintenance cycle.
	revision, err := client.SourceRevision.Get(ctx, revisionID)
	if err != nil {
		return false, err
	}
	return client.StorageMigrationItem.Query().Where(storagemigrationitem.TaskIDEQ(task.ID), storagemigrationitem.BlobIDEQ(revision.SourceBlobID)).Exist(ctx)
}

func storageEmptyProject(ctx context.Context, client *ent.Client, projectID int) error {
	reason, err := storageRebindReason(ctx, client, projectID)
	if err != nil {
		return err
	}
	if reason != "" {
		return ErrStorageConflict
	}
	return nil
}

func storageRebindReason(ctx context.Context, client *ent.Client, projectID int) (string, error) {
	has, err := client.Blob.Query().Where(blob.ProjectIDEQ(projectID), blob.StatusNEQ(blob.StatusDeleted)).Exist(ctx)
	if err != nil {
		return "", err
	}
	if !has {
		has, err = client.Resource.Query().Where(resource.ProjectIDEQ(projectID)).Exist(ctx)
		if err != nil {
			return "", err
		}
	}
	if has {
		return "project_not_empty", nil
	}
	active, err := client.StorageTask.Query().Where(storagetask.ProjectIDEQ(projectID), storagetask.StatusNotIn(storagetask.StatusCompleted, storagetask.StatusCancelled, storagetask.StatusFailed)).Exist(ctx)
	if err != nil {
		return "", err
	}
	if active {
		return "storage_operation_in_progress", nil
	}
	return "", nil
}
