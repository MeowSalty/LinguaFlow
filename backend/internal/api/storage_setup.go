package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/project"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storagenet"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/localstore"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/s3store"
	"github.com/aws/aws-sdk-go-v2/aws"
)

type storageClientEntry struct {
	identity [32]byte
	driver   storage.Driver
	http     *http.Client
}
type storageRuntime struct {
	mu          sync.Mutex
	wg          sync.WaitGroup
	clients     map[int]storageClientEntry
	local       []io.Closer
	maintenance bool
}

func (s *Server) initStorage(ctx context.Context, keys *credential.Keyring) error {
	cfg := s.serverCfg.Storage
	unfinished, err := s.entClient.StorageTask.Query().Where(storagetask.KindEQ("legacy_migration"), storagetask.PhaseNotIn("committed", "rolled_back")).Exist(ctx)
	if err != nil {
		return err
	}
	barrier, err := s.entClient.Project.Query().Where(project.StorageStateIn("legacy_migration", "legacy_rollback")).Exist(ctx)
	if err != nil {
		return err
	}
	if unfinished || barrier {
		cfg.Maintenance = true
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = filepath.Join(s.serverCfg.DataDir, "tmp")
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = filepath.Join(s.serverCfg.DataDir, "cache")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	s.storageRuntime = &storageRuntime{clients: map[int]storageClientEntry{}, maintenance: cfg.Maintenance}
	store, err := service.NewStorageService(s.entClient, s.projectSvc, cfg.WorkDir)
	if err != nil {
		return err
	}
	s.storageSvc = store
	s.storageConnections = service.NewStorageConnectionService(s.entClient, keys, cfg, s.storageFactory(cfg))
	store.SetResolver(s.storageConnections.ResolveDriver)
	backends := cfg.Backends
	if len(backends) == 0 {
		backends = []config.StorageBackendConfig{{ID: "local", Driver: "local", Root: filepath.Join(s.serverCfg.DataDir, "objects")}}
	}
	defaultID := 0
	for _, backend := range backends {
		if backend.Driver != "local" {
			continue
		}
		root, err := filepath.Abs(backend.Root)
		if err != nil {
			return err
		}
		for _, other := range backends {
			if other.Driver != "local" || other.ID == backend.ID {
				continue
			}
			otherRoot, err := filepath.Abs(other.Root)
			if err != nil {
				return err
			}
			if localRootsOverlap(root, otherRoot) {
				return service.ErrStorageConflict
			}
		}
		identityRoot := filepath.Clean(root)
		if runtime.GOOS == "windows" {
			identityRoot = strings.ToLower(identityRoot)
		}
		digest := sha256.Sum256([]byte(identityRoot))
		identity := "local-root:" + hex.EncodeToString(digest[:])
		connection, err := s.entClient.StorageConnection.Query().Where(storageconnection.OwnerKindEQ(storageconnection.OwnerKindSite), storageconnection.BackendIDEQ(backend.ID)).Only(ctx)
		if err == nil && (connection.Driver != storageconnection.DriverLocal || connection.Endpoint != "" && connection.Endpoint != identity) {
			return service.ErrStorageConflict
		}
		if err != nil && !ent.IsNotFound(err) {
			return err
		}
		var driver *localstore.Store
		if backend.ID == "legacy" || cfg.Maintenance {
			driver, err = localstore.OpenExisting(root)
		} else {
			driver, err = localstore.New(root)
		}
		if err != nil {
			if cfg.Maintenance && (errors.Is(err, os.ErrNotExist) || errors.Is(err, storage.ErrNotFound)) {
				if connection != nil && backend.ID == cfg.DefaultSiteSpace {
					space, e := s.entClient.StorageSpace.Query().Where(storagespace.ConnectionIDEQ(connection.ID)).Only(ctx)
					if e == nil {
						defaultID = space.ID
					} else if !ent.IsNotFound(e) {
						return e
					}
				}
				continue
			}
			return err
		}
		s.storageRuntime.local = append(s.storageRuntime.local, driver)
		var registered storage.Driver = driver
		if backend.ID == "legacy" {
			registered = &legacyStorageDriver{Driver: driver}
		}
		space, err := store.InstallSiteSpace(ctx, backend.ID, registered, backend.ID == "legacy")
		if err != nil {
			return err
		}
		if backend.ID == "legacy" {
			if _, err = s.entClient.StorageSpace.Update().Where(storagespace.IDEQ(space.ID), storagespace.StatusEQ(storagespace.StatusActive)).SetStatus(storagespace.StatusReadOnly).AddManagementGeneration(1).Save(ctx); err != nil {
				return err
			}
		}
		if _, err = s.entClient.StorageConnection.Update().Where(storageconnection.IDEQ(space.ConnectionID), storageconnection.EndpointEQ("")).SetEndpoint(identity).Save(ctx); err != nil {
			return err
		}
		if err = s.entClient.StorageSpace.UpdateOneID(space.ID).SetCapacityBytes(cfg.Limits.CapacityBytes).Exec(ctx); err != nil {
			return err
		}
		if backend.ID != "legacy" && !cfg.Maintenance {
			if err = s.storageConnections.AdmitLocalSpace(ctx, space.ID, driver); err != nil {
				return err
			}
		}
		if backend.ID == cfg.DefaultSiteSpace {
			defaultID = space.ID
		}
	}
	remoteDefault, err := s.storageConnections.SetupSiteBackends(ctx)
	if err != nil {
		return err
	}
	if remoteDefault != 0 {
		defaultID = remoteDefault
	}
	store.Configure(cfg, database.DialectFor(s.serverCfg.Database.Driver), defaultID)
	s.resourceSvc.SetStorage(store)
	if _, err = s.storageConnections.ValidateCurrentKeys(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Server) storageMaintenance() bool {
	return s.storageRuntime != nil && s.storageRuntime.maintenance
}

func (s *Server) storageMaintenanceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.storageMaintenance() || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case "/api/v1/auth/login", "/api/v1/auth/refresh", "/api/v1/auth/logout":
			next.ServeHTTP(w, r)
			return
		}
		s.writeStorageError(w, r, service.ErrStorageMaintenance)
	})
}

type legacyStorageDriver struct{ storage.Driver }

func (d *legacyStorageDriver) PutNew(context.Context, string, io.Reader, int64) (storage.Object, error) {
	return storage.Object{}, storage.ErrPermission
}

func localRootsOverlap(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	return a == b || strings.HasPrefix(a, b+string(filepath.Separator)) || strings.HasPrefix(b, a+string(filepath.Separator))
}

func (s *Server) storageFactory(cfg config.StorageConfig) service.StorageDriverFactory {
	return func(ctx context.Context, c *ent.StorageConnection, sp *ent.StorageSpace, p storageauth.S3Payload) (storage.Driver, error) {
		if err := p.Validate(); err != nil {
			return nil, storage.ErrAuthRequired
		}
		plain, err := json.Marshal(struct {
			Endpoint, Region, Bucket, Prefix string
			PathStyle                        bool
			Auth                             storageauth.S3Payload
		}{c.Endpoint, c.Region, sp.Bucket, sp.Prefix, c.PathStyle, p})
		if err != nil {
			return nil, storage.ErrAuthRequired
		}
		fingerprint := sha256.Sum256(plain)
		clear(plain)
		s.storageRuntime.mu.Lock()
		defer s.storageRuntime.mu.Unlock()
		if cached, ok := s.storageRuntime.clients[sp.ID]; ok && cached.identity == fingerprint {
			return cached.driver, nil
		}
		bucket := ""
		if !c.PathStyle {
			bucket = sp.Bucket
		}
		client, err := storagenet.NewClient(c.Endpoint, cfg.NetworkPolicy(bucket))
		if err != nil {
			return nil, storage.ErrPermission
		}
		provider := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: p.AccessKeyID, SecretAccessKey: p.SecretAccessKey, SessionToken: p.SessionToken, Source: "linguaflow-explicit-storage"}, nil
		})
		driver, err := s3store.New(s3store.Options{Endpoint: c.Endpoint, Region: c.Region, Bucket: sp.Bucket, Prefix: sp.Prefix, PathStyle: c.PathStyle, Credentials: provider, HTTPClient: client, MaxVersions: cfg.Limits.MaxArchiveEntries})
		if err != nil {
			client.CloseIdleConnections()
			return nil, err
		}
		if old, ok := s.storageRuntime.clients[sp.ID]; ok {
			old.http.CloseIdleConnections()
		}
		s.storageRuntime.clients[sp.ID] = storageClientEntry{identity: fingerprint, driver: driver, http: client}
		return driver, nil
	}
}

func (s *Server) startStorage(ctx context.Context) {
	if s.storageRuntime == nil || s.storageSvc == nil {
		return
	}
	s.storageRuntime.wg.Add(2)
	go func() { defer s.storageRuntime.wg.Done(); s.storageSvc.Run(ctx) }()
	go func() {
		defer s.storageRuntime.wg.Done()
		interval := s.serverCfg.Storage.ReconcileInterval
		if interval <= 0 {
			interval = time.Minute
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := s.storageConnections.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
					s.logger.Warn("storage authorization probe reconciliation deferred")
				}
			}
		}
	}()
}
func (s *Server) waitStorage(ctx context.Context) error {
	if s.storageRuntime == nil {
		return nil
	}
	done := make(chan struct{})
	go func() { s.storageRuntime.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Server) closeStorageResources() {
	if s.storageRuntime == nil {
		return
	}
	s.storageRuntime.mu.Lock()
	defer s.storageRuntime.mu.Unlock()
	for id, client := range s.storageRuntime.clients {
		client.http.CloseIdleConnections()
		delete(s.storageRuntime.clients, id)
	}
	for _, closer := range s.storageRuntime.local {
		_ = closer.Close()
	}
	s.storageRuntime.local = nil
}
