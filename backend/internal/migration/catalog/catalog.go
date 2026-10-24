// Package catalog records finite routes and their independently verified support.
package catalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sort"
	"sync"

	"entgo.io/ent/dialect/sql/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/migrate"
	"github.com/MeowSalty/LinguaFlow/backend/internal/version"
)

type Route struct {
	ID             string `json:"id"`
	Source         string `json:"source"`
	Target         string `json:"target"`
	Database       string `json:"database"`
	Mode           string `json:"mode"`
	Strategy       string `json:"strategy,omitempty"`
	DatabaseBackup string `json:"database_backup,omitempty"`
	Status         string `json:"status"`
	Instruction    string `json:"instruction"`
}

func Routes() []Route {
	return []Route{
		upgradeRoute("sqlite", "serve", "inplace", "auto", "supported", "Windows amd64: original-path conversion, committed WAL, baseline receipts and verified original restoration"),
		upgradeRoute("sqlite", "local", "inplace", "auto", "supported", "Windows amd64: original local identity, independent rehearsal and verified original-path restoration"),
		upgradeRoute("sqlite", "serve", "clone", "auto", "supported", "Windows amd64: local objects, independent target/rehearsal, source preservation and process recovery"),
		upgradeRoute("sqlite", "local", "clone", "auto", "supported", "Windows amd64: independent target/rehearsal and original local identity retained"),
		upgradeRoute("postgres", "serve", "inplace", "external", "supported", "PostgreSQL 18.6 Windows amd64: ordinary role, declaration without utilities, actual same-OID operator restore verified"),
		upgradeRoute("postgres", "serve", "inplace", "auto", "supported", "PostgreSQL 18.6 Windows amd64: source-major pg_dump-only capture; original-database restoration remains an operator action"),
		upgradeRoute("postgres", "serve", "clone", "auto", "supported", "PostgreSQL 18.6 Windows amd64: ordinary role, dump/restore and independent target/rehearsal databases"),
		storageRoute("sqlite", "serve", "auto", "supported", "Windows amd64: metadata-only protection, manifest-v1 receipt recovery and constrained rollback"),
		storageRoute("sqlite", "local", "auto", "supported", "Windows amd64: local policy/source identity, metadata-only protection and constrained rollback"),
		storageRoute("postgres", "serve", "external", "supported", "PostgreSQL 18.6 Windows amd64: declaration, manifest-v1 import and journal-loss recovery; later generations block rollback"),
		storageRoute("postgres", "serve", "auto", "supported", "PostgreSQL 18.6 Windows amd64: archive, manifest-v1 import, journal-loss recovery and constrained metadata rollback"),
		{ID: "s3-object-recovery", Source: "S3 references in upgrade/storage and retained backup facts", Target: "exact object copies or immutable COMPLIANCE versions", Database: "sqlite/postgres", Mode: "serve/local", Status: "unverified", Instruction: "SDK protocol tests passed; real provider recovery and retained versions remain unverified; inplace metadata writes retain original references"},
		{ID: "legacy-v013-v014", Source: "v0.13.0", Target: "fixed v0.14.0 tool target", Database: "sqlite/postgres", Mode: "serve/local", Status: "archived_unverified", Instruction: "use the exact v0.14.0 tool tree at 4bc4577cd669d9ccbbc4980f19e259b5beff2b18; full upgrade chain and recovery artifacts are not yet certified"},
	}
}

func upgradeRoute(database, mode, strategy, backup, status, instruction string) Route {
	return Route{ID: "upgrade-v015-auth1", Source: "v0.15.0 / initialization=1 / data=2 / auth=absent", Target: "auth=1 / data=3", Database: database, Mode: mode, Strategy: strategy, DatabaseBackup: backup, Status: status, Instruction: instruction}
}

func storageRoute(database, mode, backup, status, instruction string) Route {
	return Route{ID: "storage-legacy-import", Source: "published legacy storage_path / manifest=1", Target: "current source revisions", Database: database, Mode: mode, Strategy: "inplace", DatabaseBackup: backup, Status: status, Instruction: instruction}
}

// Bind plans to the executing binary, including development builds with dirty
// sources. A Git revision alone cannot distinguish two different tool builds.
var buildIdentity = sync.OnceValue(func() string {
	path, err := os.Executable()
	if err != nil {
		panic(err)
	}
	f, err := os.Open(path)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		panic(err)
	}
	return version.Commit + ":" + hex.EncodeToString(h.Sum(nil))
})

func Build() string { return buildIdentity() }

// Signature excludes pointer identity and defaults implemented as functions.
// The resulting digest binds the actual table/column/index/constraint contract.
func Signature(tables []*schema.Table) string {
	ordered := append([]*schema.Table(nil), tables...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	var values []any
	for _, t := range ordered {
		cols := []any{}
		for _, c := range t.Columns {
			cols = append(cols, struct {
				Name, Type, Attr, Collation string
				Nullable, Unique, Increment bool
				Size                        int64
				Enums                       []string
				Default                     any
				SchemaType                  map[string]string
			}{c.Name, c.Type.String(), c.Attr, c.Collation, c.Nullable, c.Unique, c.Increment, c.Size, c.Enums, c.Default, c.SchemaType})
		}
		indices := []any{}
		for _, x := range t.Indexes {
			names := []string{}
			for _, c := range x.Columns {
				names = append(names, c.Name)
			}
			indices = append(indices, struct {
				Name       string
				Unique     bool
				Columns    []string
				Annotation any
			}{x.Name, x.Unique, names, x.Annotation})
		}
		foreign := []any{}
		primary := []string{}
		for _, column := range t.PrimaryKey {
			primary = append(primary, column.Name)
		}
		for _, x := range t.ForeignKeys {
			names, refs := []string{}, []string{}
			for _, c := range x.Columns {
				names = append(names, c.Name)
			}
			for _, c := range x.RefColumns {
				refs = append(refs, c.Name)
			}
			ref := ""
			if x.RefTable != nil {
				ref = x.RefTable.Name
			}
			foreign = append(foreign, struct {
				Symbol, Table, OnDelete, OnUpdate string
				Columns, RefColumns               []string
			}{x.Symbol, ref, string(x.OnDelete), string(x.OnUpdate), names, refs})
		}
		values = append(values, struct {
			Name                      string
			Schema                    string
			PrimaryKey                []string
			Columns, Indices, Foreign []any
			Annotation                any
		}{t.Name, t.Schema, primary, cols, indices, foreign, t.Annotation})
	}
	data, err := json.Marshal(values)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ent's schema builder mutates table descriptors. Freeze before any connection
// is opened so repeated apply/resume use the same generated schema contract.
var targetSignature = Signature(migrate.Tables)

func TargetSignature() string { return targetSignature }
