package operation

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"
)

const Version = 1

type Endpoint struct {
	ConfigPath  string `json:"config_path,omitempty"`
	DataDir     string `json:"data_dir"`
	Driver      string `json:"driver"`
	Mode        string `json:"mode"`
	Identity    string `json:"identity"`
	InputDigest string `json:"input_digest,omitempty"`
	DatabaseDSN string `json:"database_dsn,omitempty"` // SQLite only; never a connection secret.
}

type LocalRoot struct {
	BackendID    string `json:"backend_id"`
	Source       string `json:"source"`
	Target       string `json:"target"`
	Identity     string `json:"identity,omitempty"`
	MarkerDigest string `json:"marker_digest,omitempty"`
}

type Account struct {
	UserID   int     `json:"user_id"`
	Username string  `json:"username"`
	Email    *string `json:"email"`
}

type Target struct {
	DataDir              string `json:"data_dir,omitempty"`
	ConfigPath           string `json:"config_path,omitempty"`
	RehearsalConfigPath  string `json:"rehearsal_config_path,omitempty"`
	ParentIdentity       string `json:"parent_identity"`
	Identity             string `json:"identity,omitempty"`
	InputDigest          string `json:"input_digest,omitempty"`
	RehearsalIdentity    string `json:"rehearsal_identity,omitempty"`
	RehearsalDataDir     string `json:"rehearsal_data_dir,omitempty"`
	RehearsalInputDigest string `json:"rehearsal_input_digest,omitempty"`
}

type Plan struct {
	Version           int              `json:"version"`
	ID                string           `json:"operation_id"`
	Kind              string           `json:"kind"`
	Route             string           `json:"route"`
	Build             string           `json:"build_commit"`
	CreatedAt         time.Time        `json:"created_at"`
	Source            Endpoint         `json:"source"`
	SourceFingerprint string           `json:"source_fingerprint"`
	SourceSchema      string           `json:"source_schema"`
	ObjectsDigest     string           `json:"objects_digest,omitempty"`
	TargetSignature   string           `json:"target_signature"`
	Target            Target           `json:"target"`
	Accounts          []Account        `json:"accounts,omitempty"`
	LegacyRoot        string           `json:"legacy_root,omitempty"`
	StorageManifest   string           `json:"storage_manifest,omitempty"`
	StorageDigest     string           `json:"storage_digest,omitempty"`
	ImportedManifest  string           `json:"imported_manifest,omitempty"`
	ImportedDigest    string           `json:"imported_digest,omitempty"`
	StorageBaseline   string           `json:"storage_baseline,omitempty"`
	StorageMaxIDs     map[string]int64 `json:"storage_max_ids,omitempty"`
	ExternalReceipt   string           `json:"external_receipt,omitempty"`
	ExternalDigest    string           `json:"external_digest,omitempty"`
	LocalRoots        []LocalRoot      `json:"local_roots,omitempty"`
}

type Journal struct {
	Version           int       `json:"version"`
	ID                string    `json:"operation_id"`
	Kind              string    `json:"kind"`
	PlanDigest        string    `json:"plan_digest"`
	TargetSignature   string    `json:"target_signature"`
	Build             string    `json:"build_commit"`
	Phase             string    `json:"phase"`
	Staging           string    `json:"staging,omitempty"`
	StagingIdentity   string    `json:"staging_identity,omitempty"`
	RestoreIdentity   string    `json:"restore_identity,omitempty"`
	Steps             []string  `json:"steps,omitempty"`
	ErrorCode         string    `json:"error_code,omitempty"`
	UpdatedAt         time.Time `json:"updated_at"`
	TargetFingerprint string    `json:"target_fingerprint,omitempty"`
	BackupDirectory   string    `json:"backup_directory,omitempty"`
}

type Report struct {
	Version           int              `json:"version"`
	ID                string           `json:"operation_id"`
	Phase             string           `json:"phase"`
	Verified          bool             `json:"verified"`
	SourceFingerprint string           `json:"source_fingerprint"`
	Counts            map[string]int64 `json:"counts,omitempty"`
	NeedsAction       []string         `json:"needs_action,omitempty"`
	Next              []string         `json:"next,omitempty"`
	BackupTime        time.Time        `json:"backup_time,omitempty"`
}

func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func Digest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func ValidHex(s string, n int) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == n/2 && len(s) == n
}

func (p *Plan) Validate() error {
	if p.Version != Version || !ValidHex(p.ID, 32) || p.Build == "" || !ValidHex(p.SourceFingerprint, 64) || !ValidHex(p.SourceSchema, 64) || !ValidHex(p.TargetSignature, 64) {
		return Fail(Incompatible, "preserve the plan; use its exact tool build")
	}
	if p.Kind != "upgrade" && p.Kind != "storage" {
		return Fail(Incompatible, "unknown operation kind")
	}
	if (p.Kind == "upgrade" && p.Route != "upgrade-v015-auth1") || (p.Kind == "storage" && p.Route != "storage-legacy-import") {
		return Fail(Incompatible, "unknown conversion route")
	}
	if (p.Source.Driver != "sqlite" && p.Source.Driver != "postgres") || (p.Source.Mode != "serve" && p.Source.Mode != "local") || !filepath.IsAbs(p.Source.DataDir) || p.Source.Identity == "" {
		return Fail(Incompatible, "invalid source identity")
	}
	if p.Source.ConfigPath != "" && !filepath.IsAbs(p.Source.ConfigPath) {
		return Fail(Incompatible, "configuration paths must be absolute")
	}
	if p.Kind == "upgrade" && ((p.Source.Driver == "sqlite" && (!filepath.IsAbs(p.Target.DataDir) || p.Target.ParentIdentity == "")) || (p.Source.Driver == "postgres" && (!filepath.IsAbs(p.Target.ConfigPath) || p.Target.Identity == ""))) {
		return Fail(Incompatible, "invalid independent target")
	}
	if !ValidHex(p.Source.InputDigest, 64) || !ValidHex(p.ObjectsDigest, 64) || p.CreatedAt.IsZero() {
		return Fail(Incompatible, "source inputs and object baseline must be bound")
	}
	for _, path := range []string{p.LegacyRoot, p.StorageManifest, p.ImportedManifest, p.ExternalReceipt, p.Target.RehearsalConfigPath} {
		if path != "" && !filepath.IsAbs(path) {
			return Fail(Incompatible, "operation artifact paths must be absolute")
		}
	}
	if (p.StorageManifest != "" && !ValidHex(p.StorageDigest, 64)) || (p.ImportedManifest != "" && !ValidHex(p.ImportedDigest, 64)) || (p.ExternalReceipt != "" && !ValidHex(p.ExternalDigest, 64)) {
		return Fail(Incompatible, "artifact digest is missing")
	}
	if p.Kind == "storage" && (p.StorageManifest == "" || !ValidHex(p.StorageBaseline, 64) || len(p.Accounts) > 0) {
		return Fail(Incompatible, "storage plan baseline is incomplete")
	}
	if p.Kind == "upgrade" && len(p.Accounts) == 0 {
		return Fail(Incompatible, "upgrade requires the complete account mapping")
	}
	backends := map[string]bool{}
	for _, root := range p.LocalRoots {
		if root.BackendID == "" || backends[root.BackendID] || !filepath.IsAbs(root.Source) || !filepath.IsAbs(root.Target) || root.MarkerDigest != "" && !ValidHex(root.MarkerDigest, 64) {
			return Fail(Incompatible, "invalid local storage identity")
		}
		backends[root.BackendID] = true
	}
	seen := map[int]bool{}
	for _, a := range p.Accounts {
		if a.UserID <= 0 || a.Username == "" || seen[a.UserID] {
			return errors.New("invalid account mapping")
		}
		seen[a.UserID] = true
	}
	return nil
}

func (j *Journal) Bind(p *Plan) error {
	if j.Version != Version || j.ID != p.ID || j.Kind != p.Kind || j.PlanDigest != Digest(p) || j.TargetSignature != p.TargetSignature || j.Build != p.Build {
		return Fail(Incompatible, "journal does not belong to this immutable plan")
	}
	valid := map[string]bool{"planned": true, "backing_up": true, "staging": true, "converting": true, "verifying": true, "ready": true, "publishing": true, "completed": true, "needs_action": true, "abandoned": true, "rolled_back": true}
	if !valid[j.Phase] {
		return Fail(Incompatible, "unknown journal phase")
	}
	return nil
}
