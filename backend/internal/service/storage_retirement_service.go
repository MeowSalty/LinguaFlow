package service

import (
	"context"
	"crypto/rand"
	"fmt"
	"sort"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagepathclaim"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageguard"
)

type StorageRetirementService struct {
	client      *ent.Client
	storage     *StorageService
	connections *StorageConnectionService
	cfg         config.StorageConfig
	previewKey  [32]byte
	local       bool
	runMu       sync.Mutex
	executing   map[int]bool
}

func NewStorageRetirementService(storage *StorageService, connections *StorageConnectionService, local ...bool) (*StorageRetirementService, error) {
	s := &StorageRetirementService{client: storage.client, storage: storage, connections: connections, cfg: connections.cfg, executing: map[int]bool{}}
	if len(local) > 0 {
		s.local = local[0]
	}
	if _, err := rand.Read(s.previewKey[:]); err != nil {
		return nil, err
	}
	connections.ioGuard = storage.ioGuard
	connections.retirements = s
	storage.retirements = s
	return s, nil
}

func retirementListLifecycle(in []string) (string, error) {
	if len(in) == 0 {
		return "", nil
	}
	if len(in) != 1 {
		return "", ErrInvalidInput
	}
	switch in[0] {
	case "", "available", "retiring", "removed", "all":
		return in[0], nil
	}
	return "", ErrInvalidInput
}

// ensureStoragePathClaim never treats a missing claim as proof of free space.
// Callers hold the bucket namespace gate and inspect all live historical rows.
func ensureStoragePathClaim(ctx context.Context, c *ent.Client, sp *ent.StorageSpace) error {
	if sp.Bucket == "" || sp.Lifecycle == storagespace.LifecycleRemoved {
		return nil
	}
	claim, err := c.StoragePathClaim.Query().Where(storagepathclaim.SpaceIDEQ(sp.ID)).Only(ctx)
	if err == nil {
		if claim.Identity != sp.Identity || claim.Bucket != sp.Bucket || claim.Prefix != sp.Prefix {
			return ErrStorageRetirementConflict
		}
		return c.StoragePathClaim.UpdateOneID(claim.ID).SetExecutionGeneration(sp.ExecutionGeneration).Exec(ctx)
	}
	if !ent.IsNotFound(err) {
		return err
	}
	return c.StoragePathClaim.Create().SetSpaceID(sp.ID).SetIdentity(sp.Identity).SetBucket(sp.Bucket).SetPrefix(sp.Prefix).SetExecutionGeneration(sp.ExecutionGeneration).Exec(ctx)
}

func (s *StorageRetirementService) ReconcilePathClaims(ctx context.Context) error {
	return withOrganizationTransaction(ctx, s.client, func(tx *ent.Client) error {
		spaces, err := tx.StorageSpace.Query().Where(storagespace.LifecycleNEQ(storagespace.LifecycleRemoved), storagespace.HasConnectionWith(storageconnection.DriverEQ(storageconnection.DriverS3))).All(ctx)
		if err != nil {
			return err
		}
		sort.Slice(spaces, func(i, j int) bool {
			if spaces[i].Bucket != spaces[j].Bucket {
				return spaces[i].Bucket < spaces[j].Bucket
			}
			return spaces[i].ID < spaces[j].ID
		})
		for i, sp := range spaces {
			if err = storageguard.LockNamespace(ctx, tx, sp.Bucket); err != nil {
				return err
			}
			for j := 0; j < i; j++ {
				if spaces[j].Bucket == sp.Bucket && storagePrefixesOverlap(spaces[j].Prefix, sp.Prefix) {
					return fmt.Errorf("%w: overlapping active storage identities", ErrStorageRetirementConflict)
				}
			}
			if err = ensureStoragePathClaim(ctx, tx, sp); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *StorageRetirementService) authorized(ctx context.Context, c *ent.Client, actor, id int) (*ent.StorageRetirement, error) {
	r, err := c.StorageRetirement.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err = s.connections.requireOwner(ctx, c, actor, string(r.OwnerKind), r.OwnerID); err != nil {
		return nil, err
	}
	return r, nil
}
