package operation

import (
	"path/filepath"
	"time"
)

// Baseline records digests rather than released business rows or secret bytes.
// Changes contain only the route's declared mutable metadata and its expectations.
type Baseline struct {
	Version           int               `json:"version"`
	ID                string            `json:"operation_id"`
	PlanDigest        string            `json:"plan_digest"`
	SourceFingerprint string            `json:"source_fingerprint"`
	SourceSchema      string            `json:"source_schema"`
	SourceIdentity    string            `json:"source_identity"`
	InputDigest       string            `json:"input_digest"`
	ConversionTime    time.Time         `json:"conversion_time"`
	Tables            []TableBaseline   `json:"tables"`
	Changes           []RowChange       `json:"changes,omitempty"`
	Facts             map[string]string `json:"facts,omitempty"`
}

type TableBaseline struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	PrimaryKey []string `json:"primary_key"`
	Count      int64    `json:"count"`
	MaxID      int64    `json:"max_id,omitempty"`
	NumericID  bool     `json:"numeric_id"`
	Keys       string   `json:"keys_digest"`
	Preserved  string   `json:"preserved_digest"`
}

type RowChange struct {
	Table    string            `json:"table"`
	ID       int               `json:"id"`
	Before   map[string]string `json:"before"`
	Expected map[string]string `json:"expected"`
}

// Restore binds the same-volume recovery intent before any database file moves.
type Restore struct {
	Version                   int      `json:"version"`
	ID                        string   `json:"operation_id"`
	PlanDigest                string   `json:"plan_digest"`
	BaselineDigest            string   `json:"baseline_digest"`
	Phase                     string   `json:"phase"`
	Directory                 string   `json:"directory"`
	DirectoryIdentity         string   `json:"directory_identity"`
	DatabasePath              string   `json:"database_path"`
	DatabaseDirectoryIdentity string   `json:"database_directory_identity,omitempty"`
	DatabaseIdentity          string   `json:"database_identity,omitempty"`
	ReplacementIdentity       string   `json:"replacement_identity,omitempty"`
	OriginalIdentity          string   `json:"original_identity"`
	RestoredIdentity          string   `json:"restored_identity,omitempty"`
	FailedDatabase            Artifact `json:"failed_database"`
	Replacement               Artifact `json:"replacement"`
}

func (b *Baseline) Bind(p *Plan) error {
	if b.Version != Version || b.ID != p.ID || b.PlanDigest != Digest(p) || b.SourceFingerprint != p.SourceFingerprint || b.SourceSchema != p.SourceSchema || b.SourceIdentity != p.Source.Identity || b.InputDigest != p.Source.InputDigest || b.ConversionTime.IsZero() || len(b.Tables) == 0 {
		return Fail(Incompatible, "baseline does not bind the original source and immutable plan")
	}
	seen := map[string]bool{}
	for _, t := range b.Tables {
		if t.Name == "" || seen[t.Name] || len(t.Columns) == 0 || len(t.PrimaryKey) == 0 || t.Count < 0 || !ValidHex(t.Keys, 64) || !ValidHex(t.Preserved, 64) {
			return Fail(Incompatible, "invalid baseline table evidence")
		}
		seen[t.Name] = true
	}
	return nil
}

func LoadBaseline(directory string, p *Plan, j *Journal) error {
	b := new(Baseline)
	if err := Read(filepath.Join(directory, "baseline.json"), b); err != nil {
		return Fail(BackupIncomplete, "original baseline is missing; retain the database and operation")
	}
	if err := b.Bind(p); err != nil {
		return err
	}
	if j.BaselineDigest != "" && j.BaselineDigest != Digest(b) {
		return Fail(Incompatible, "baseline digest changed")
	}
	p.Baseline = b
	return nil
}

func BaselineDigest(p *Plan) string {
	if p.Baseline == nil {
		return ""
	}
	return Digest(p.Baseline)
}

func ResolveStrategy(value Strategy, target bool) (Strategy, error) {
	if value == "" {
		if target {
			return Clone, nil
		}
		return Inplace, nil
	}
	if value != Inplace && value != Clone {
		return "", Fail(Incompatible, "strategy must be inplace or clone")
	}
	if value == Inplace && target {
		return "", Fail(UnsupportedTarget, "inplace conflicts with independent target arguments")
	}
	return value, nil
}

func ResolveBackup(value DatabaseBackupMode, driver string, strategy Strategy) (DatabaseBackupMode, error) {
	if value == "" {
		if driver == "postgres" && strategy == Inplace {
			return BackupExternal, nil
		}
		return BackupAuto, nil
	}
	if value != BackupAuto && value != BackupExternal {
		return "", Fail(Incompatible, "database-backup must be auto or external")
	}
	if value == BackupExternal && (driver != "postgres" || strategy != Inplace) {
		return "", Fail(UnsupportedTarget, "external database protection is only available for PostgreSQL inplace")
	}
	return value, nil
}
