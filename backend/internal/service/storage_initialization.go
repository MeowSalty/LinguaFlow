package service

import (
	"context"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
)

// InitializeSiteStorage atomically records policy and deployment identities.
// Filesystem materialization and provider access happen after this transaction.
func (s *StorageService) InitializeSiteStorage(ctx context.Context, local bool, cfg config.StorageConfig, localIdentities map[string]string) error {
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		if err := ensureStoragePolicy(ctx, tx, local, cfg.Initialization.CapacityBytes.Set, cfg.Initialization.CapacityBytes.Value, cfg.Initialization.LogicalLimitBytes.Set, cfg.Initialization.LogicalLimitBytes.Value); err != nil {
			return err
		}
		for backendID, identity := range localIdentities {
			sp, err := installSiteSpace(ctx, tx, backendID, backendID == "legacy")
			if err != nil {
				return err
			}
			c, err := tx.StorageConnection.Get(ctx, sp.ConnectionID)
			if err != nil {
				return err
			}
			if c.Endpoint != "" && c.Endpoint != identity {
				return ErrStorageConflict
			}
			if c.Endpoint == "" {
				if err = tx.StorageConnection.UpdateOneID(c.ID).SetEndpoint(identity).Exec(ctx); err != nil {
					return err
				}
			}
			if backendID == "legacy" && sp.Status == storagespace.StatusActive {
				if _, err = tx.StorageSpace.Update().Where(storagespace.IDEQ(sp.ID), storagespace.ManagementGenerationEQ(sp.ManagementGeneration)).SetStatus(storagespace.StatusReadOnly).AddManagementGeneration(1).Save(ctx); err != nil {
					return err
				}
			}
			if c.Driver != storageconnection.DriverLocal {
				return ErrStorageConflict
			}
		}
		connections := &StorageConnectionService{client: tx, cfg: cfg}
		_, err := connections.setupSiteBackends(ctx, tx)
		return err
	})
}
