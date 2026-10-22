package service

import (
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageguard"
)

// Retirement has its own version: support for S3 or project migration does not
// imply that the complete connection-removal protocol has been implemented.
const StorageRetirementContractVersion = 1

var (
	ErrStorageRetiring                        = storageguard.ErrRetiring
	ErrStorageRemoved                         = storageguard.ErrRemoved
	ErrStorageRetirementConflict              = storageguard.ErrConflict
	ErrStorageRetirementPreviewStale          = errors.New("storage_retirement_preview_stale")
	ErrStorageRetirementDependenciesRemaining = errors.New("storage_retirement_dependencies_remaining")
	ErrStorageRetirementConfigurationPending  = errors.New("storage_retirement_configuration_pending")
)

// StorageRetirementCapabilities distinguishes the two independently accepted
// release stages. Services without a retirement coordinator keep both closed.
type StorageRetirementCapabilities struct {
	ContractVersion int                       `json:"contract_version"`
	UnusedRemoval   StorageActionAvailability `json:"unused_removal"`
	InUseRetirement StorageActionAvailability `json:"in_use_retirement"`
}

func storageRetirementCapabilities() StorageRetirementCapabilities {
	return StorageRetirementCapabilities{
		ContractVersion: StorageRetirementContractVersion,
		UnusedRemoval:   storageRetirementAvailability(),
		InUseRetirement: storageRetirementAvailability(),
	}
}

func storageRetirementAvailability() StorageActionAvailability {
	return storageAvailability([]string{"storage_capability_unsupported"})
}

func (s *StorageRetirementService) capabilities() StorageRetirementCapabilities {
	reasons := []string{}
	if s.local {
		reasons = append(reasons, "storage_capability_unsupported")
	}
	if s.cfg.Maintenance {
		reasons = append(reasons, "storage_maintenance")
	}
	return StorageRetirementCapabilities{ContractVersion: 1, UnusedRemoval: storageAvailability(reasons), InUseRetirement: storageAvailability(reasons)}
}
