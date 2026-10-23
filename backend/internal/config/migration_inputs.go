package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"
)

// MigrationDatabaseInputs contains only the inputs required for a read-only
// format probe. JWT, keyrings, bootstrap and storage secrets remain unread.
type MigrationDatabaseInputs struct {
	Database                  DatabaseConfig
	DataDir, ConfigPath, Mode string
}

func (r MigrationDatabaseInputs) DatabaseDSN() string {
	c := ServerConfig{DataDir: r.DataDir, Database: r.Database}
	return c.DatabaseDSN()
}

func ResolveMigrationDatabase(in ServerInputs) (*MigrationDatabaseInputs, error) {
	mode := in.Mode
	if mode == ModeServer {
		mode = "serve"
	}
	if mode != "serve" && mode != ModeLocal {
		return nil, errors.New("mode must be serve or local")
	}
	cwd := in.WorkingDirectory
	var err error
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	r := &MigrationDatabaseInputs{Mode: mode, Database: defaultDatabaseConfig(DatabaseDriverSQLite), DataDir: absolutePath("data", cwd)}
	if mode == ModeLocal {
		ucd := in.UserConfigDir
		if ucd == "" {
			ucd, err = os.UserConfigDir()
			if err != nil {
				return nil, err
			}
		}
		r.DataDir = absolutePath(filepath.Join(ucd, "LinguaFlow"), cwd)
	}
	path, selected := in.Environment["LINGUAFLOW_SERVER_CONFIG"]
	if in.ConfigPath != nil {
		path, selected = *in.ConfigPath, true
	}
	values := map[string]string{}
	origins := map[string]string{}
	if selected {
		if strings.TrimSpace(path) == "" {
			return nil, errors.New("config path must not be empty")
		}
		r.ConfigPath = absolutePath(path, cwd)
		f, err := os.Open(r.ConfigPath)
		if err != nil {
			return nil, fmt.Errorf("read deployment document: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(f, 4<<20+1))
		f.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(data) > 4<<20 {
			return nil, errors.New("deployment document is too large")
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		var doc yaml.Node
		if err := dec.Decode(&doc); err != nil {
			return nil, errors.New("invalid deployment YAML")
		}
		var extra yaml.Node
		if err := dec.Decode(&extra); err != io.EOF {
			return nil, errors.New("deployment YAML must contain one document")
		}
		if len(doc.Content) != 1 {
			return nil, errors.New("deployment YAML requires a mapping")
		}
		root, err := migrationMapping(doc.Content[0])
		if err != nil {
			return nil, err
		}
		if node, ok := root["server"]; ok {
			server, err := migrationMapping(node)
			if err != nil {
				return nil, err
			}
			if n, ok := server["data_dir"]; ok {
				v, err := migrationScalar(n)
				if err != nil {
					return nil, err
				}
				values["data_dir"], origins["data_dir"] = v, filepath.Dir(r.ConfigPath)
			}
			if mode != ModeLocal {
				if n, ok := server["database"]; ok {
					database, err := migrationMapping(n)
					if err != nil {
						return nil, err
					}
					for _, key := range []string{"driver", "dsn"} {
						if n, ok := database[key]; ok {
							v, err := migrationScalar(n)
							if err != nil {
								return nil, err
							}
							values[key], origins[key] = v, filepath.Dir(r.ConfigPath)
						}
					}
				}
			}
		}
	}
	for key, environment := range map[string]string{"data_dir": "LINGUAFLOW_DATA_DIR", "driver": "LINGUAFLOW_DATABASE_DRIVER", "dsn": "LINGUAFLOW_DATABASE_DSN"} {
		if mode == ModeLocal && key != "data_dir" {
			continue
		}
		value, present := in.Environment[environment]
		if key == "dsn" {
			_, file := in.Environment[environment+"_FILE"]
			if present || file {
				value, present, err = EnvironmentSecret(in.Environment, environment, cwd)
				if err != nil {
					return nil, errors.New("database DSN secret input is invalid")
				}
			}
		}
		if present {
			values[key], origins[key] = value, cwd
		}
	}
	// Offline tools must never silently select the runtime default database or
	// local user directory. A deployment document, DSN or data root is an
	// explicit source choice; list/help do not enter this resolver.
	_, overrideDataDir := in.Overrides["server.data_dir"]
	_, selectedDataDir := values["data_dir"]
	selectedDSN := strings.TrimSpace(values["dsn"]) != ""
	if !selected && !overrideDataDir && !selectedDataDir && !selectedDSN {
		return nil, errors.New("select an explicit source configuration, database DSN or data directory")
	}
	if value, ok := in.Overrides["server.data_dir"]; ok {
		v, ok := value.(string)
		if !ok {
			return nil, errors.New("invalid data directory override")
		}
		values["data_dir"], origins["data_dir"] = v, cwd
	}
	if value, ok := values["data_dir"]; ok {
		if strings.Contains(value, "${") {
			value, err = ExpandReferences(value, in.Environment)
			if err != nil {
				return nil, errors.New("data directory interpolation is invalid")
			}
		}
		if strings.TrimSpace(value) == "" {
			return nil, errors.New("data directory must be explicit and nonempty")
		}
		r.DataDir = absolutePath(value, origins["data_dir"])
	}
	if value, ok := values["driver"]; ok {
		if value != "sqlite" && value != "postgres" {
			return nil, errors.New("database driver must be sqlite or postgres")
		}
		r.Database = defaultDatabaseConfig(value)
	}
	if r.Database.Driver != "sqlite" && r.Database.Driver != "postgres" {
		return nil, errors.New("database driver must be sqlite or postgres")
	}
	if value, ok := values["dsn"]; ok {
		if strings.Contains(value, "${") {
			expanded, err := ExpandReferences(value, in.Environment)
			if err != nil {
				return nil, errors.New("database DSN interpolation is invalid")
			}
			value = expanded
		}
		if r.Database.Driver == "sqlite" && value != "" {
			value, err = migrationSQLitePath(value, origins["dsn"])
			if err != nil {
				return nil, err
			}
		}
		r.Database.DSN = value
	}
	if r.Database.Driver == "postgres" {
		if _, err := pgx.ParseConfig(r.Database.DSN); err != nil || strings.TrimSpace(r.Database.DSN) == "" {
			return nil, errors.New("PostgreSQL database connection input is invalid")
		}
	}
	return r, nil
}

func migrationMapping(n *yaml.Node) (map[string]*yaml.Node, error) {
	if n.Kind != yaml.MappingNode {
		return nil, errors.New("database connection input must be a mapping")
	}
	out := map[string]*yaml.Node{}
	for i := 0; i < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Tag != "!!str" || out[k.Value] != nil {
			return nil, errors.New("duplicate or invalid connection input key")
		}
		out[k.Value] = v
	}
	return out, nil
}
func migrationScalar(n *yaml.Node) (string, error) {
	if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
		return "", errors.New("database connection fields must be strings")
	}
	return n.Value, nil
}
func migrationSQLitePath(dsn, origin string) (string, error) {
	path, query, found := strings.Cut(dsn, "?")
	if path == "" || path == ":memory:" || strings.ContainsRune(path, 0) {
		return "", errors.New("offline migration requires a disk SQLite database")
	}
	prefix := ""
	if strings.HasPrefix(path, "file:") {
		prefix = "file:"
		path = strings.TrimPrefix(path, "file:")
		var err error
		path, err = url.PathUnescape(path)
		if err != nil {
			return "", errors.New("invalid SQLite database path")
		}
	}
	if strings.HasPrefix(path, "//") {
		return "", errors.New("network SQLite database paths are unsupported")
	}
	path = absolutePath(filepath.FromSlash(path), origin)
	if prefix != "" {
		path = prefix + url.PathEscape(filepath.ToSlash(path))
	}
	if found {
		path += "?" + query
	}
	return path, nil
}
