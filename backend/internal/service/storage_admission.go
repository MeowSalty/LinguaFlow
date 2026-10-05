package service

import (
	"errors"
	"sort"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

var ErrStorageDeploymentDisabled = errors.New("storage_deployment_disabled")

// StorageRuntime reflects the configuration used by the running services,
// including maintenance imposed at startup by unfinished offline recovery.
type StorageRuntime struct {
	DeploymentEnabled bool `json:"deployment_enabled"`
	Maintenance       bool `json:"maintenance"`
}

type StorageActionAvailability struct {
	Allowed     bool     `json:"allowed"`
	ReasonCodes []string `json:"reason_codes"`
}

func storageRuntimeState(cfg config.StorageConfig) StorageRuntime {
	return StorageRuntime{DeploymentEnabled: cfg.Enabled, Maintenance: cfg.Maintenance}
}

func (s *StorageService) Runtime() StorageRuntime           { return storageRuntimeState(s.cfg) }
func (s *StorageConnectionService) Runtime() StorageRuntime { return storageRuntimeState(s.cfg) }

type storageOperation uint8

const (
	storageOpRead storageOperation = iota
	storageOpWrite
	storageOpDelete
	storageOpCreateConnection
	storageOpCreateSpace
	storageOpAuthorizeRead
	storageOpAuthorizeWrite
	storageOpCheckRead
	storageOpCheckWrite
	storageOpRevokeAuth
	storageOpSetStatus
	storageOpSaveSitePolicy
	storageOpSaveUserPolicy
)

// storageOperationReasons checks only the deployment boundary. Callers check
// ownership before using it and add the state/authorization rules of the action.
// Reading and exact deletion deliberately do not inherit new-object admission.
func storageOperationReasons(runtime StorageRuntime, op storageOperation, conn *ent.StorageConnection) []string {
	reasons := []string{}
	maintenance, deployment := false, false
	switch op {
	case storageOpCreateConnection, storageOpCreateSpace, storageOpAuthorizeWrite, storageOpCheckWrite:
		maintenance, deployment = true, true
	case storageOpAuthorizeRead, storageOpDelete:
		maintenance = true
	case storageOpWrite:
		maintenance = true
		deployment = conn == nil || conn.Driver != storageconnection.DriverLocal || conn.OwnerKind != storageconnection.OwnerKindSite || conn.AuthSource != storageconnection.AuthSourceDeployment
	case storageOpSaveUserPolicy:
		deployment = true
	}
	if maintenance && runtime.Maintenance {
		reasons = append(reasons, "storage_maintenance")
	}
	if deployment && !runtime.DeploymentEnabled {
		reasons = append(reasons, "storage_deployment_disabled")
	}
	return reasons
}

func storageReasonPriority(code string) int {
	switch code {
	case "storage_maintenance":
		return 0
	case "storage_deployment_disabled", "byos_disabled":
		return 1
	case "policy_disallowed", "storage_policy_violation":
		return 2
	case "storage_capability_unsupported":
		return 3
	case "connection_disabled":
		return 4
	case "storage_space_required", "storage_check_space_limit_exceeded":
		return 5
	case "space_disabled", "space_read_only":
		return 6
	case "storage_auth_required", "space_unverified":
		return 7
	case "storage_crypto_unavailable":
		return 8
	case "storage_permission_denied":
		return 9
	case "storage_quota_exceeded":
		return 10
	default:
		return 11
	}
}

func normalizeStorageReasons(reasons []string) []string {
	out := []string{}
	for _, reason := range reasons {
		out = appendReason(out, reason)
	}
	sort.SliceStable(out, func(i, j int) bool { return storageReasonPriority(out[i]) < storageReasonPriority(out[j]) })
	return out
}

func storageAvailability(reasons []string) StorageActionAvailability {
	reasons = normalizeStorageReasons(reasons)
	return StorageActionAvailability{Allowed: len(reasons) == 0, ReasonCodes: reasons}
}

func storageAdmissionError(reasons []string) error {
	reasons = normalizeStorageReasons(reasons)
	if len(reasons) == 0 {
		return nil
	}
	switch reasons[0] {
	case "storage_maintenance", "space_read_only":
		return ErrStorageMaintenance
	case "storage_deployment_disabled", "byos_disabled":
		return ErrStorageDeploymentDisabled
	case "policy_disallowed", "storage_policy_violation":
		return ErrStoragePolicy
	case "storage_capability_unsupported":
		return storage.ErrUnsupported
	case "storage_space_required", "storage_check_space_limit_exceeded":
		return ErrInvalidInput
	case "storage_auth_required", "space_unverified":
		return storage.ErrAuthRequired
	case "storage_crypto_unavailable":
		return ErrStorageCrypto
	case "connection_disabled", "space_disabled", "storage_permission_denied":
		return storage.ErrPermission
	case "storage_quota_exceeded":
		return storage.ErrLimit
	case "storage_operation_in_progress":
		return ErrStorageInProgress
	case "project_not_empty":
		return ErrStorageConflict
	default:
		return storage.ErrUnavailable
	}
}
