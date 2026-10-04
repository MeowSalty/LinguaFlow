package storagemigrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/blob"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/bloblocation"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/resource"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/sourcerevision"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/localstore"
)

var ErrChanged = errors.New("storage migration baseline changed; preserve the manifest and review the affected resource")

func (m *Migrator) checkOffline(manifest *Manifest) error {
	if !m.options.Offline || !m.options.BackupConfirmed {
		return errors.New("apply/resume/rollback require explicit offline and database/keyring backup confirmation")
	}
	if err := manifest.validate(); err != nil {
		return err
	}
	if manifest.LegacyRoot != m.options.LegacyRoot || manifest.DefaultRoot != m.options.DefaultRoot || manifest.DefaultBackendID != m.options.DefaultBackendID {
		return errors.New("manifest roots or default backend do not match current deployment inputs")
	}
	return nil
}

func migrationFingerprint(manifest *Manifest) string {
	type baseline struct {
		ResourceID                              int
		Digest, Key, SHA, Verification          string
		Size                                    *int64
		SourceGeneration, TranslationGeneration int64
		Rejected                                bool
	}
	var entries []baseline
	for _, entry := range manifest.Entries {
		entries = append(entries, baseline{entry.ResourceID, entry.DatabaseDigest, entry.ObjectKey, entry.ObservedSHA256, entry.Verification, entry.ObservedSize, entry.SourceGeneration, entry.TranslationGeneration, entry.Rejected})
	}
	return digest(struct {
		Root, DefaultRoot, Backend string
		Entries                    []baseline
		Projects                   []ProjectCheckpoint
		Jobs                       []JobCheckpoint
	}{manifest.LegacyRoot, manifest.DefaultRoot, manifest.DefaultBackendID, entries, baselineProjects(manifest.Projects), manifest.Jobs})
}

func baselineProjects(projects []ProjectCheckpoint) []ProjectCheckpoint {
	result := append([]ProjectCheckpoint(nil), projects...)
	for i := range result {
		result[i].Applied = false
	}
	return result
}

// Apply 同时也是恢复（resume）实现。每个资源都有一个确定性的
// Blob 标识；丢失的检查点可从已提交的数据库行重建。
func (m *Migrator) Apply(ctx context.Context, manifest *Manifest, checkpoint func() error) error {
	if err := m.checkOffline(manifest); err != nil {
		return err
	}
	if manifest.Phase == "rolled_back" || manifest.Phase == "rolling_back" {
		return errors.New("cannot apply a manifest being rolled back")
	}
	if err := database.WithMigrationLock(ctx, m.db, m.dialect, func(client *ent.Client) error { return client.Schema.Create(ctx) }); err != nil {
		return err
	}
	root, err := localstore.OpenExisting(manifest.LegacyRoot)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	if root != nil {
		defer root.Close()
	}
	for _, entry := range manifest.Entries {
		if entry.Rejected {
			continue
		}
		row, err := m.client.Resource.Get(ctx, entry.ResourceID)
		if err != nil {
			return err
		}
		current, err := snapshotEnt(ctx, m.client, row)
		if err != nil {
			return err
		}
		if digest(current) != entry.DatabaseDigest || row.TranslationGeneration != entry.TranslationGeneration {
			return ErrChanged
		}
		if err := m.checkObserved(ctx, &entry, root, current.Segments); err != nil {
			return err
		}
	}
	if err := m.prepare(ctx, manifest); err != nil {
		return err
	}
	manifest.Phase = "applying"
	if err := checkpoint(); err != nil {
		return err
	}
	for i := range manifest.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := &manifest.Entries[i]
		if entry.Rejected {
			continue
		}
		if err := m.applyEntry(ctx, manifest, entry, root); err != nil {
			return fmt.Errorf("resource %d: %w", entry.ResourceID, err)
		}
		if err := checkpoint(); err != nil {
			return err
		}
	}
	if err := m.finish(ctx, manifest); err != nil {
		return err
	}
	manifest.Phase = "applied"
	return checkpoint()
}

func transaction(ctx context.Context, client *ent.Client, fn func(*ent.Client) error) error {
	tx, err := client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx.Client()); err != nil {
		return err
	}
	return tx.Commit()
}

func (m *Migrator) prepare(ctx context.Context, manifest *Manifest) error {
	return transaction(ctx, m.client, func(client *ent.Client) error {
		op, err := client.StorageTask.Query().Where(storagetask.OperationIDEQ(manifest.OperationID)).Only(ctx)
		if err == nil {
			if op.Kind != "legacy_migration" || op.RequestHash != migrationFingerprint(manifest) || op.Phase == "rolled_back" || op.Phase == "rolling_back" {
				return ErrChanged
			}
			manifest.LegacySpaceID = number(op.Input["legacy_space_id"])
			manifest.DefaultSpaceID = number(op.Input["default_space_id"])
			return nil
		}
		if !ent.IsNotFound(err) {
			return err
		}
		legacy, err := m.ensureSpace(ctx, client, "legacy", true)
		if err != nil {
			return err
		}
		current, err := m.ensureSpace(ctx, client, manifest.DefaultBackendID, false)
		if err != nil {
			return err
		}
		manifest.LegacySpaceID = legacy.ID
		manifest.DefaultSpaceID = current.ID
		for i := range manifest.Projects {
			p := &manifest.Projects[i]
			row, err := client.Project.Get(ctx, p.ID)
			if err != nil {
				return err
			}
			if row.StorageGeneration != p.Generation || !equalID(row.StorageSpaceID, p.PreviousSpaceID) || row.StorageState != "active" {
				return ErrChanged
			}
			update := client.Project.Update().Where(project.IDEQ(p.ID), project.StorageGenerationEQ(p.Generation)).SetStorageState("legacy_migration").AddStorageGeneration(1)
			if p.PreviousSpaceID == nil {
				update.SetStorageSpaceID(current.ID)
			}
			n, err := update.Save(ctx)
			if err != nil {
				return err
			}
			if n != 1 {
				return ErrChanged
			}
		}
		for _, prior := range manifest.Jobs {
			row, err := client.Job.Get(ctx, prior.ID)
			if err != nil {
				return err
			}
			if row.Status != prior.Status || !equalString(row.ErrorMessage, prior.Error) {
				return ErrChanged
			}
			if err := client.Job.UpdateOneID(prior.ID).SetStatus("failed").SetErrorMessage(blockedJobError).Exec(ctx); err != nil {
				return err
			}
		}
		input := map[string]any{"legacy_space_id": legacy.ID, "default_space_id": current.ID, "legacy_root": manifest.LegacyRoot, "default_root": manifest.DefaultRoot, "generation_protocol": 2}
		_, err = client.StorageTask.Create().SetOperationID(manifest.OperationID).SetIdempotencyKey(manifest.OperationID).SetRequestHash(migrationFingerprint(manifest)).SetKind("legacy_migration").SetStatus(storagetask.StatusNeedsAction).SetPhase("applying").SetErrorCode("offline_migration_in_progress").SetInput(input).Save(ctx)
		return err
	})
}

func (m *Migrator) ensureSpace(ctx context.Context, client *ent.Client, backendID string, legacy bool) (*ent.StorageSpace, error) {
	connection, err := client.StorageConnection.Query().Where(storageconnection.BackendIDEQ(backendID), storageconnection.OwnerKindEQ(storageconnection.OwnerKindSite)).Only(ctx)
	if ent.IsNotFound(err) {
		connection, err = client.StorageConnection.Create().SetName(backendID).SetBackendID(backendID).SetDriver(storageconnection.DriverLocal).Save(ctx)
	}
	if err != nil {
		return nil, err
	}
	if connection.Driver != storageconnection.DriverLocal || connection.Status != storageconnection.StatusEnabled {
		return nil, errors.New("offline migration requires enabled local deployment backends")
	}
	space, err := client.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(connection.ID)).Only(ctx)
	if err == nil {
		if legacy && space.Status != storagespace.StatusReadOnly {
			return nil, errors.New("existing legacy space must be read_only")
		}
		if !legacy && (space.Status != storagespace.StatusActive || !space.Verified) {
			return nil, errors.New("default local space is not writable and verified")
		}
		return space, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	identity, err := newID()
	if err != nil {
		return nil, err
	}
	nonce, err := newID()
	if err != nil {
		return nil, err
	}
	create := client.StorageSpace.Create().SetConnectionID(connection.ID).SetName(backendID).SetIdentity(identity).SetMarkerNonce(nonce).SetVerified(legacy).SetCapacityBytes(m.options.CapacityBytes)
	if legacy {
		create.SetStatus(storagespace.StatusReadOnly)
	}
	return create.Save(ctx)
}

func (m *Migrator) applyEntry(ctx context.Context, manifest *Manifest, entry *Entry, root *localstore.Store) error {
	identity := fmt.Sprintf("legacy-%s-%d", manifest.OperationID, entry.ResourceID)
	// 发布前重新校验字节。缺失/未验证的文件会被保留，
	// 但若文件在清单盘点与 apply 之间发生变化，则需要重新盘点。
	if !entry.Applied && entry.ObservedSize != nil {
		if root == nil {
			return ErrChanged
		}
		row, err := m.client.Resource.Get(ctx, entry.ResourceID)
		if err != nil {
			return err
		}
		snapshot, err := snapshotEnt(ctx, m.client, row)
		if err != nil {
			return err
		}
		if err := m.checkObserved(ctx, entry, root, snapshot.Segments); err != nil {
			return err
		}
	}
	var blobID, locationID, revisionID int
	err := transaction(ctx, m.client, func(client *ent.Client) error {
		row, err := client.Resource.Get(ctx, entry.ResourceID)
		if err != nil {
			return err
		}
		snapshot, err := snapshotEnt(ctx, client, row)
		if err != nil {
			return err
		}
		if digest(snapshot) != entry.DatabaseDigest || row.TranslationGeneration != entry.TranslationGeneration {
			return ErrChanged
		}
		existing, err := client.Blob.Query().Where(blob.IdentityEQ(identity)).Only(ctx)
		if err == nil {
			if row.CurrentSourceRevisionID == nil || row.SourceGeneration != entry.SourceGeneration+1 {
				return ErrChanged
			}
			revision, err := client.SourceRevision.Get(ctx, *row.CurrentSourceRevisionID)
			if err != nil {
				return err
			}
			if revision.SourceBlobID != existing.ID || existing.ActiveLocationID == nil || existing.LocationGeneration != 1 {
				return ErrChanged
			}
			blobID = existing.ID
			locationID = *existing.ActiveLocationID
			revisionID = revision.ID
			return nil
		}
		if !ent.IsNotFound(err) {
			return err
		}
		if row.CurrentSourceRevisionID != nil || row.SourceGeneration != entry.SourceGeneration {
			return ErrChanged
		}
		owner, err := client.Project.Get(ctx, entry.ProjectID)
		if err != nil {
			return err
		}
		if owner.StorageState != "legacy_migration" {
			return ErrChanged
		}
		kind, ownerID := blob.OwnerKindSite, 0
		if owner.OwnerUserID != nil {
			kind = blob.OwnerKindUser
			ownerID = *owner.OwnerUserID
		} else if owner.OwnerOrgID != nil {
			kind = blob.OwnerKindOrg
			ownerID = *owner.OwnerOrgID
		} else {
			return errors.New("legacy project ownership is unproven")
		}
		create := client.Blob.Create().SetIdentity(identity).SetProjectID(entry.ProjectID).SetOwnerKind(kind).SetOwnerID(ownerID).SetPurpose(blob.PurposeSource)
		if entry.Verification == "verified" {
			if entry.ObservedSize == nil || entry.ObservedSHA256 == "" {
				return ErrChanged
			}
			create.SetSize(*entry.ObservedSize).SetSha256(entry.ObservedSHA256)
		}
		object, err := create.Save(ctx)
		if err != nil {
			return err
		}
		bytes := int64(0)
		if entry.ObservedSize != nil {
			bytes = *entry.ObservedSize
		}
		location, err := client.BlobLocation.Create().SetBlobID(object.ID).SetSpaceID(manifest.LegacySpaceID).SetObjectKey(entry.ObjectKey).SetSize(bytes).SetStatus(bloblocation.StatusLive).SetIntegrity(bloblocation.Integrity(entry.Integrity)).Save(ctx)
		if err != nil {
			return err
		}
		if entry.Integrity == "available" {
			if err := client.BlobLocation.UpdateOneID(location.ID).SetVerifiedAt(manifest.CreatedAt).Exec(ctx); err != nil {
				return err
			}
		}
		if err := client.Blob.UpdateOneID(object.ID).SetActiveLocationID(location.ID).SetLocationGeneration(1).SetStatus(blob.StatusReady).Exec(ctx); err != nil {
			return err
		}
		revisionCreate := client.SourceRevision.Create().SetResourceID(entry.ResourceID).SetProjectID(entry.ProjectID).SetSourceBlobID(object.ID).SetFormat(entry.Format).SetParserVersion("legacy_unknown").SetVerificationState(sourcerevision.VerificationState(entry.Verification))
		if entry.Verification == "verified" {
			revisionCreate.SetSize(*entry.ObservedSize).SetSha256(entry.ObservedSHA256)
		}
		revision, err := revisionCreate.Save(ctx)
		if err != nil {
			return err
		}
		n, err := client.Resource.Update().Where(resource.IDEQ(entry.ResourceID), resource.CurrentSourceRevisionIDIsNil(), resource.SourceGenerationEQ(entry.SourceGeneration), resource.TranslationGenerationEQ(entry.TranslationGeneration)).SetCurrentSourceRevisionID(revision.ID).AddSourceGeneration(1).Save(ctx)
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrChanged
		}
		if err := client.StorageSpace.UpdateOneID(manifest.LegacySpaceID).AddLiveBytes(bytes).Exec(ctx); err != nil {
			return err
		}
		blobID = object.ID
		locationID = location.ID
		revisionID = revision.ID
		return nil
	})
	if err == nil {
		entry.BlobID = blobID
		entry.LocationID = locationID
		entry.RevisionID = revisionID
		entry.Applied = true
	}
	return err
}

func (m *Migrator) checkObserved(ctx context.Context, entry *Entry, root *localstore.Store, segments []segmentSnapshot) error {
	if entry.Applied || entry.ObservedSize == nil {
		return nil
	}
	if root == nil {
		return ErrChanged
	}
	check := *entry
	m.inspectFile(ctx, root, &check, segments)
	if check.Rejected || check.Verification != entry.Verification || check.ObservedSize == nil || *check.ObservedSize != *entry.ObservedSize || check.ObservedSHA256 != entry.ObservedSHA256 || check.Integrity != "available" {
		return ErrChanged
	}
	return nil
}

func (m *Migrator) finish(ctx context.Context, manifest *Manifest) error {
	return transaction(ctx, m.client, func(client *ent.Client) error {
		op, err := client.StorageTask.Query().Where(storagetask.OperationIDEQ(manifest.OperationID)).Only(ctx)
		if err != nil {
			return err
		}
		for i := range manifest.Projects {
			p := &manifest.Projects[i]
			row, err := client.Project.Get(ctx, p.ID)
			if err != nil {
				return err
			}
			if op.Phase == "committed" {
				expected := p.Generation + 1 // pre-contract completed operations
				if number(op.Input["generation_protocol"]) >= 2 {
					expected++
				}
				if row.StorageState != "active" || row.StorageGeneration != expected {
					return ErrChanged
				}
			} else if row.StorageState == "legacy_migration" && row.StorageGeneration == p.Generation+1 {
				if err := client.Project.UpdateOneID(p.ID).SetStorageState("active").AddStorageGeneration(1).Exec(ctx); err != nil {
					return err
				}
			} else {
				return ErrChanged
			}
			p.Applied = true
		}
		if op.Phase == "committed" {
			return nil
		}
		input := op.Input
		input["generation_protocol"] = 2
		_, err = client.StorageTask.UpdateOneID(op.ID).SetInput(input).SetStatus(storagetask.StatusCompleted).SetPhase("committed").SetErrorCode("").Save(ctx)
		return err
	})
}

func number(value any) int {
	switch value := value.(type) {
	case int:
		return value
	case float64:
		return int(value)
	case json.Number:
		n, _ := value.Int64()
		return int(n)
	}
	return 0
}
func equalID(a, b *int) bool        { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func equalString(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
