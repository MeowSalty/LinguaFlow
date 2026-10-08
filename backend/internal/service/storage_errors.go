package service

import (
	"context"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/diskspace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
	"strings"
)

var (
	ErrStorageExpired    = errors.New("storage_intent_expired")
	ErrStorageInProgress = errors.New("storage_operation_in_progress")
)

// StorageOperationError carries only authorized, durable operation identities.
// Callers must obtain the operation through the actor's project authorization.
type StorageOperationError struct {
	Err         error
	TaskID      int
	OperationID string
	CheckID     int
}

func (e *StorageOperationError) Error() string { return StorageErrorCode(e.Err) }
func (e *StorageOperationError) Unwrap() error { return e.Err }

func ValidateStorageIdempotencyKey(key string) error {
	if len(key) == 0 || len(key) > 200 || strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") {
		return ErrInvalidInput
	}
	return nil
}

// StorageErrorCode is the common, redacted directory for HTTP, tasks and batches.
func StorageErrorCode(err error) string {
	err = diskspace.Classify(err)
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, ErrStorageExpired):
		return "storage_intent_expired"
	case errors.Is(err, ErrStorageInProgress):
		return "storage_operation_in_progress"
	case errors.Is(err, ErrStorageIdempotency):
		return "storage_idempotency_conflict"
	case errors.Is(err, ErrSourceRevisionConflict):
		return "source_revision_conflict"
	case errors.Is(err, ErrStorageConflict):
		return "storage_generation_conflict"
	case errors.Is(err, ErrRepairMismatch):
		return "repair_content_mismatch"
	case errors.Is(err, ErrStorageMaintenance):
		return "storage_maintenance"
	case errors.Is(err, ErrStorageDeploymentDisabled):
		return "storage_deployment_disabled"
	case errors.Is(err, ErrStoragePolicy):
		return "storage_policy_violation"
	case errors.Is(err, ErrStorageTooLarge), errors.Is(err, storage.ErrPayloadTooLarge):
		return "storage_payload_too_large"
	case errors.Is(err, storage.ErrDiskSpaceInsufficient):
		return "storage_disk_insufficient"
	case errors.Is(err, storage.ErrDiskSpaceUnknown):
		return "storage_disk_probe_failed"
	case errors.Is(err, storage.ErrLimit):
		return "storage_quota_exceeded"
	case errors.Is(err, storage.ErrNotFound):
		return "source_missing"
	case errors.Is(err, storage.ErrCorrupt):
		return "source_corrupt"
	case errors.Is(err, storage.ErrPermission):
		return "storage_permission_denied"
	case errors.Is(err, storage.ErrAuthRequired):
		return "storage_auth_required"
	case errors.Is(err, ErrStorageCrypto):
		return "storage_crypto_unavailable"
	case errors.Is(err, storage.ErrUnsupported):
		return "storage_capability_unsupported"
	case errors.Is(err, ErrUnsupportedFormat), errors.Is(err, ErrParseFailed):
		return "storage_parse_failed"
	case errors.Is(err, ErrInvalidInput), errors.Is(err, ErrResourcePathInvalid), errors.Is(err, storageauth.ErrInvalid), errors.Is(err, storage.ErrInvalidKey):
		return "invalid_input"
	case errors.Is(err, context.DeadlineExceeded):
		return "storage_timeout"
	case errors.Is(err, ErrStorageCancelled):
		return "storage_cancelled"
	// A disconnected request does not prove cancellation of the durable task.
	case errors.Is(err, context.Canceled):
		return "storage_unavailable"
	default:
		return "storage_unavailable"
	}
}
