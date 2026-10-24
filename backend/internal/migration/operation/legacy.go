package operation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const legacyVersion = 1

// LegacyStatus exposes the immutable v0.16 operation documents for JSON status
// output. Its contents are never upgraded or written by this package.
type LegacyStatus struct {
	Version       int      `json:"version"`
	Kind          string   `json:"kind"`
	ExpectedBuild string   `json:"expected_build"`
	Plan          any      `json:"plan"`
	Journal       any      `json:"journal"`
	ReportPath    string   `json:"report_path"`
	Next          []string `json:"next"`
}

// These DTOs are frozen from v0.16.0. Field order and tags are part of the
// plan digest contract and must not follow the current operation DTOs.
type legacyEndpoint struct {
	ConfigPath  string `json:"config_path,omitempty"`
	DataDir     string `json:"data_dir"`
	Driver      string `json:"driver"`
	Mode        string `json:"mode"`
	Identity    string `json:"identity"`
	InputDigest string `json:"input_digest,omitempty"`
	DatabaseDSN string `json:"database_dsn,omitempty"`
}

type legacyLocalRoot struct {
	BackendID    string `json:"backend_id"`
	Source       string `json:"source"`
	Target       string `json:"target"`
	Identity     string `json:"identity,omitempty"`
	MarkerDigest string `json:"marker_digest,omitempty"`
}

type legacyAccount struct {
	UserID   int     `json:"user_id"`
	Username string  `json:"username"`
	Email    *string `json:"email"`
}

type legacyTarget struct {
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

type legacyPlan struct {
	Version           int               `json:"version"`
	ID                string            `json:"operation_id"`
	Kind              string            `json:"kind"`
	Route             string            `json:"route"`
	Build             string            `json:"build_commit"`
	CreatedAt         time.Time         `json:"created_at"`
	Source            legacyEndpoint    `json:"source"`
	SourceFingerprint string            `json:"source_fingerprint"`
	SourceSchema      string            `json:"source_schema"`
	ObjectsDigest     string            `json:"objects_digest,omitempty"`
	TargetSignature   string            `json:"target_signature"`
	Target            legacyTarget      `json:"target"`
	Accounts          []legacyAccount   `json:"accounts,omitempty"`
	LegacyRoot        string            `json:"legacy_root,omitempty"`
	StorageManifest   string            `json:"storage_manifest,omitempty"`
	StorageDigest     string            `json:"storage_digest,omitempty"`
	ImportedManifest  string            `json:"imported_manifest,omitempty"`
	ImportedDigest    string            `json:"imported_digest,omitempty"`
	StorageBaseline   string            `json:"storage_baseline,omitempty"`
	StorageMaxIDs     map[string]int64  `json:"storage_max_ids,omitempty"`
	ExternalReceipt   string            `json:"external_receipt,omitempty"`
	ExternalDigest    string            `json:"external_digest,omitempty"`
	LocalRoots        []legacyLocalRoot `json:"local_roots,omitempty"`
}

type legacyJournal struct {
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

type legacyReport struct {
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

// DocumentVersion returns plan.json's top-level version without interpreting
// any other fields, so callers can route newer operation formats safely.
func DocumentVersion(directory string) (int, error) {
	data, err := readLegacyFile(filepath.Join(directory, "plan.json"))
	if err != nil {
		return 0, err
	}
	if err := rejectDuplicates(json.NewDecoder(strings.NewReader(string(data)))); err != nil {
		return 0, Fail(Incompatible, "duplicate or malformed plan.json")
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil || document == nil {
		return 0, Fail(Incompatible, "invalid plan.json document")
	}
	raw, ok := document["version"]
	if !ok {
		return 0, Fail(Incompatible, "plan.json version is missing")
	}
	var version int
	if err := json.Unmarshal(raw, &version); err != nil || version < 1 {
		return 0, Fail(Incompatible, "plan.json version is invalid")
	}
	return version, nil
}

// ReadLegacyStatus reads and validates a v0.16 operation without changing any
// operation file. A missing journal is reported as missing evidence, not as a
// verified or completed operation.
func ReadLegacyStatus(directory string) (*LegacyStatus, error) {
	if err := ValidatePath(directory, true); err != nil {
		return nil, err
	}
	planPath := filepath.Join(directory, "plan.json")
	data, err := readLegacyFile(planPath)
	if err != nil {
		return nil, err
	}
	version, err := legacyDocumentVersion(data)
	if err != nil {
		return nil, err
	}
	if version != legacyVersion {
		return nil, Fail(Incompatible, "operation is not v0.16 version 1; use the exact build recorded in its plan")
	}
	plan := new(legacyPlan)
	if err := Decode(data, plan); err != nil {
		return nil, err
	}
	if err := plan.validate(); err != nil {
		return nil, err
	}

	status := &LegacyStatus{
		Version:       legacyVersion,
		Kind:          plan.Kind,
		ExpectedBuild: plan.Build,
		Plan:          plan,
		ReportPath:    filepath.Join(directory, "report.json"),
		Next: []string{
			"This reader is read-only; preserve the operation directory and use the exact recorded build " + plan.Build + " for resume or recovery.",
		},
	}
	if plan.Kind == "upgrade" {
		status.Next = append(status.Next, "v0.16 upgrade version 1 follows the clone contract; its target is independent of the source.")
	} else {
		status.Next = append(status.Next, "v0.16 storage version 1 retains its original source and import contract.")
	}

	journalPath := filepath.Join(directory, "journal.json")
	journalData, err := readLegacyFile(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		status.Next = append(status.Next, "journal.json is missing at "+journalPath+"; progress and completion cannot be established. Preserve the plan and use the recorded build for recovery.")
		return status, nil
	}
	if err != nil {
		return nil, err
	}
	journal := new(legacyJournal)
	if err := Decode(journalData, journal); err != nil {
		return nil, err
	}
	if err := journal.bind(plan); err != nil {
		return nil, err
	}
	status.Journal = journal

	reportData, err := readLegacyFile(status.ReportPath)
	if errors.Is(err, os.ErrNotExist) {
		status.Next = append(status.Next, "report.json is not present; this reader cannot establish completion or current database state.")
		return status, nil
	}
	if err != nil {
		return nil, err
	}
	report := new(legacyReport)
	if err := Decode(reportData, report); err != nil {
		return nil, err
	}
	if report.Version != legacyVersion || report.ID != plan.ID || report.SourceFingerprint != plan.SourceFingerprint {
		return nil, Fail(Incompatible, "report does not belong to this immutable plan")
	}
	status.Next = append(status.Next, report.Next...)
	return status, nil
}

func legacyDocumentVersion(data []byte) (int, error) {
	if err := rejectDuplicates(json.NewDecoder(strings.NewReader(string(data)))); err != nil {
		return 0, Fail(Incompatible, "duplicate or malformed plan.json")
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil || document == nil {
		return 0, Fail(Incompatible, "invalid plan.json document")
	}
	raw, ok := document["version"]
	if !ok {
		return 0, Fail(Incompatible, "plan.json version is missing")
	}
	var version int
	if err := json.Unmarshal(raw, &version); err != nil || version < 1 {
		return 0, Fail(Incompatible, "plan.json version is invalid")
	}
	return version, nil
}

func readLegacyFile(path string) ([]byte, error) {
	if err := ValidatePath(path, false); err != nil {
		return nil, err
	}
	f, err := openRead(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 64<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 64<<20 {
		return nil, Fail(Incompatible, "document exceeds size limit")
	}
	return data, nil
}

func (p *legacyPlan) validate() error {
	if p.Version != legacyVersion || !legacyValidHex(p.ID, 32) || p.Build == "" || !legacyValidHex(p.SourceFingerprint, 64) || !legacyValidHex(p.SourceSchema, 64) || !legacyValidHex(p.TargetSignature, 64) {
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
	if !legacyValidHex(p.Source.InputDigest, 64) || !legacyValidHex(p.ObjectsDigest, 64) || p.CreatedAt.IsZero() {
		return Fail(Incompatible, "source inputs and object baseline must be bound")
	}
	for _, path := range []string{p.LegacyRoot, p.StorageManifest, p.ImportedManifest, p.ExternalReceipt, p.Target.RehearsalConfigPath} {
		if path != "" && !filepath.IsAbs(path) {
			return Fail(Incompatible, "operation artifact paths must be absolute")
		}
	}
	if (p.StorageManifest != "" && !legacyValidHex(p.StorageDigest, 64)) || (p.ImportedManifest != "" && !legacyValidHex(p.ImportedDigest, 64)) || (p.ExternalReceipt != "" && !legacyValidHex(p.ExternalDigest, 64)) {
		return Fail(Incompatible, "artifact digest is missing")
	}
	if p.Kind == "storage" && (p.StorageManifest == "" || !legacyValidHex(p.StorageBaseline, 64) || len(p.Accounts) > 0) {
		return Fail(Incompatible, "storage plan baseline is incomplete")
	}
	if p.Kind == "upgrade" && len(p.Accounts) == 0 {
		return Fail(Incompatible, "upgrade requires the complete account mapping")
	}
	backends := map[string]bool{}
	for _, root := range p.LocalRoots {
		if root.BackendID == "" || backends[root.BackendID] || !filepath.IsAbs(root.Source) || !filepath.IsAbs(root.Target) || root.MarkerDigest != "" && !legacyValidHex(root.MarkerDigest, 64) {
			return Fail(Incompatible, "invalid local storage identity")
		}
		backends[root.BackendID] = true
	}
	seen := map[int]bool{}
	for _, account := range p.Accounts {
		if account.UserID <= 0 || account.Username == "" || seen[account.UserID] {
			return errors.New("invalid account mapping")
		}
		seen[account.UserID] = true
	}
	return nil
}

func (j *legacyJournal) bind(p *legacyPlan) error {
	if j.Version != legacyVersion || j.ID != p.ID || j.Kind != p.Kind || j.PlanDigest != legacyDigest(p) || j.TargetSignature != p.TargetSignature || j.Build != p.Build {
		return Fail(Incompatible, "journal does not belong to this immutable plan")
	}
	valid := map[string]bool{"planned": true, "backing_up": true, "staging": true, "converting": true, "verifying": true, "ready": true, "publishing": true, "completed": true, "needs_action": true, "abandoned": true, "rolled_back": true}
	if !valid[j.Phase] {
		return Fail(Incompatible, "unknown journal phase")
	}
	return nil
}

func legacyDigest(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func legacyValidHex(value string, length int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == length/2 && len(value) == length
}
