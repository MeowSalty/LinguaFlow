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
	ID          string `json:"id"`
	Source      string `json:"source"`
	Target      string `json:"target"`
	Database    string `json:"database"`
	Mode        string `json:"mode"`
	Status      string `json:"status"`
	Instruction string `json:"instruction"`
}

func Routes() []Route {
	return []Route{
		{"upgrade-v015-auth1", "v0.15.0 / initialization=1 / data=2 / auth=absent", "auth=1 / data=3", "sqlite", "serve", "supported", "local objects; exact release and binary acceptance passed; perform deployment/business checks before enabling writers"},
		{"upgrade-v015-auth1", "v0.15.0 / initialization=1 / data=2 / auth=absent", "auth=1 / data=3", "sqlite", "local", "supported", "local objects; isolated local identity, original passwords, rehearsal and recovery accepted"},
		{"upgrade-v015-auth1", "v0.15.0 / initialization=1 / data=2 / auth=absent", "auth=1 / data=3", "postgres", "serve", "unverified", "requires matching pg_dump/pg_restore and an independent empty target"},
		{"storage-legacy-import", "published legacy storage_path / manifest=1", "current source revisions", "sqlite", "serve", "supported", "local objects; manifest-v1 read-only takeover, resource commit recovery, repeated resume and constrained rollback accepted"},
		{"storage-legacy-import", "published legacy storage_path / manifest=1", "current source revisions", "sqlite", "local", "supported", "local objects; local identity, account policy, source facts and rollback accepted separately"},
		{"storage-legacy-import", "published legacy storage_path / manifest=1", "current source revisions", "postgres", "serve", "unverified", "real PostgreSQL import and constrained rollback acceptance are required"},
		{"s3-object-recovery", "S3 references in upgrade/storage and retained backup facts", "exact object copies or immutable COMPLIANCE versions", "sqlite/postgres", "serve/local", "unverified", "SDK protocol tests passed; real provider recovery and retained versions are still unverified; an actual fingerprint-bound external receipt is mandatory"},
		{"legacy-v013-v014", "v0.13.0", "fixed v0.14.0 tool target", "sqlite/postgres", "serve/local", "archived_unverified", "use the exact v0.14.0 tool tree at 4bc4577cd669d9ccbbc4980f19e259b5beff2b18; full upgrade chain and recovery artifacts are not yet certified"},
	}
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
