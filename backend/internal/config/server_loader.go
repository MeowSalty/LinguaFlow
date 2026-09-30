package config

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

// ServerInputs contains only explicit command inputs. A nil ConfigPath means
// no flag was supplied; an empty Environment is an intentionally empty snapshot.
type ServerInputs struct {
	Mode             string
	ConfigPath       *string
	Environment      map[string]string
	WorkingDirectory string
	UserConfigDir    string
	Overrides        map[string]any
	OverrideSources  map[string]string
	AllowNetwork     bool
}

type FieldExplanation struct {
	FieldInfo
	Value  string
	Source string
}

// ResolvedServer is the immutable result of offline input resolution. Startup
// copies Config before injecting prepared secrets. Bootstrap is not retained by
// business services, and no field below represents a bound network address.
type ResolvedServer struct {
	Config             ServerConfig
	Log                LogConfig
	Bootstrap          BootstrapInput
	Fields             []FieldExplanation
	LocalSecretPath    string
	LocalSecretPending bool
	KeyringPending     bool
	AllowNetwork       bool
	ConfigPath         string
}

type sourcedValue struct {
	value  any
	source string
}

func ResolveServerConfig(in ServerInputs) (*ResolvedServer, error) {
	if in.Mode == "serve" {
		in.Mode = ModeServer
	}
	if in.Mode != ModeServer && in.Mode != ModeLocal {
		return nil, fmt.Errorf("mode must be serve or local")
	}
	cwd := in.WorkingDirectory
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve working directory: %w", err)
		}
	}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}
	r := &ResolvedServer{Config: *DefaultServerConfig(), Log: LogConfig{Level: "info", Format: "text"}, AllowNetwork: in.AllowNetwork}
	r.Config.Mode = in.Mode
	if in.Mode == ModeLocal {
		r.Config.Host = "127.0.0.1"
		r.Config.Port = 18080
		r.Config.DataDir = ""
	}
	if in.Mode != ModeLocal && in.AllowNetwork {
		return nil, fmt.Errorf("allow-network is only available in local mode")
	}
	values := map[string]sourcedValue{}
	path, selected := in.Environment["LINGUAFLOW_SERVER_CONFIG"]
	if in.ConfigPath != nil {
		path = *in.ConfigPath
		selected = true
	}
	if selected {
		if strings.TrimSpace(path) == "" {
			return nil, fmt.Errorf("config path must not be empty")
		}
		path = absolutePath(path, cwd)
		r.ConfigPath = path
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read deployment document: %w", err)
		}
		if err := decodeServerDocument(data, path, in.Environment, in.Mode, values); err != nil {
			return nil, err
		}
	}
	for _, f := range serverFields {
		if in.Mode == ModeLocal && strings.HasPrefix(f.Key, "server.database.") {
			continue
		}
		direct, hasDirect := in.Environment[f.Environment]
		fileName, hasFile := in.Environment[f.Environment+"_FILE"]
		if !f.Sensitive {
			hasFile = false
		}
		if !hasDirect && !hasFile {
			continue
		}
		if f.Modes == "serve" && in.Mode == ModeLocal {
			return nil, fmt.Errorf("%s is not applicable to local mode", f.Key)
		}
		if hasDirect && hasFile {
			return nil, fmt.Errorf("%s: %s conflicts with %s_FILE", f.Key, f.Environment, f.Environment)
		}
		source := "env: " + f.Environment
		if hasFile {
			if strings.TrimSpace(fileName) == "" {
				return nil, fmt.Errorf("%s: secret file path must not be empty", f.Key)
			}
			content, err := os.ReadFile(absolutePath(fileName, cwd))
			if err != nil {
				return nil, fmt.Errorf("%s: cannot read secret file", f.Key)
			}
			direct = removeFinalNewline(string(content))
			if direct == "" {
				return nil, fmt.Errorf("%s: secret file is empty", f.Key)
			}
			source += "_FILE (path relative to working directory)"
		}
		v, err := parseEnvironmentValue(f, direct)
		if err != nil {
			return nil, fmt.Errorf("%s (%s): %w", f.Key, f.Environment, err)
		}
		if f.Type == "path" && v.(string) != "" {
			v = absolutePath(v.(string), cwd)
			source += " (relative to working directory)"
		}
		values[f.Key] = sourcedValue{v, source}
	}
	for key, v := range in.Overrides {
		f, ok := findServerField(key)
		if !ok || !flagField(key) {
			return nil, fmt.Errorf("unsupported deployment flag field %s", key)
		}
		if !validValueType(f.Type, v) {
			return nil, fmt.Errorf("%s: invalid flag value type", key)
		}
		if f.Type == "path" && v.(string) != "" {
			v = absolutePath(v.(string), cwd)
		}
		source := "flag: --" + flagName(key)
		if f.Type == "path" {
			source += " (relative to working directory)"
		}
		if explicitSource := in.OverrideSources[key]; explicitSource != "" {
			source = explicitSource
		}
		values[key] = sourcedValue{v, source}
	}
	for _, f := range serverFields {
		if v, ok := values[f.Key]; ok {
			f.write(r, v.value)
		}
	}
	if in.Mode == ModeLocal {
		if _, explicit := values["server.data_dir"]; !explicit {
			ucd := in.UserConfigDir
			if ucd == "" {
				ucd, err = os.UserConfigDir()
				if err != nil {
					return nil, fmt.Errorf("resolve local data directory: %w", err)
				}
			}
			r.Config.DataDir = filepath.Join(ucd, "LinguaFlow")
		}
	}
	// Defaults depending on another field are applied only after its final value.
	dbDefaults := defaultDatabaseConfig(r.Config.Database.Driver)
	if _, ok := values["server.database.max_open_conns"]; !ok {
		r.Config.Database.MaxOpenConns = dbDefaults.MaxOpenConns
	}
	if _, ok := values["server.database.max_idle_conns"]; !ok {
		r.Config.Database.MaxIdleConns = dbDefaults.MaxIdleConns
	}
	if _, ok := values["server.database.conn_max_lifetime"]; !ok {
		r.Config.Database.ConnMaxLifetime = dbDefaults.ConnMaxLifetime
	}
	if _, ok := values["server.workers.translation.queue_capacity"]; !ok {
		r.Config.Workers.Translation.QueueCapacity = r.Config.Workers.Translation.Count * 4
	}
	if _, ok := values["server.workers.sync.queue_capacity"]; !ok {
		r.Config.Workers.Sync.QueueCapacity = r.Config.Workers.Sync.Count * 8
	}
	if _, ok := values["server.sse.max_replay_events"]; !ok {
		r.Config.SSE.MaxReplayEvents = r.Config.SSE.RingBufferCapacity * 2
	}
	if r.Config.DataDir != "" {
		r.Config.DataDir = absolutePath(r.Config.DataDir, cwd)
	}
	// Validate paths and capacities before using derived dependency paths. A
	// missing local secret is resolved separately, never as a placeholder value.
	if err := validateServerConfig(&r.Config, false); err != nil {
		return nil, err
	}
	if in.Mode == ModeLocal {
		if err := validateLocalHost(r.Config.Host, in.AllowNetwork); err != nil {
			return nil, err
		}
		if _, explicit := values["server.jwt_secret"]; !explicit {
			r.LocalSecretPath = filepath.Join(r.Config.DataDir, "instance-secret")
			secret, err := ReadLocalSecret(r.LocalSecretPath)
			if errors.Is(err, os.ErrNotExist) {
				r.LocalSecretPending = true
			} else if err != nil {
				return nil, err
			} else {
				r.Config.JWTSecret = secret
			}
		}
		if _, explicit := values["server.credentials.keyring_file"]; !explicit {
			r.Config.Credentials.KeyringFile = filepath.Join(r.Config.DataDir, "credentials-keyring.json")
		}
	}
	if err := validateServerConfig(&r.Config, !r.LocalSecretPending); err != nil {
		return nil, err
	}
	if r.Log.Level != "debug" && r.Log.Level != "info" && r.Log.Level != "warn" && r.Log.Level != "error" {
		return nil, fmt.Errorf("log.level must be debug, info, warn or error")
	}
	if r.Log.Format != "text" && r.Log.Format != "json" {
		return nil, fmt.Errorf("log.format must be text or json")
	}
	if strings.TrimSpace(r.Config.Credentials.KeyringFile) == "" {
		return nil, fmt.Errorf("server.credentials.keyring_file is required in serve mode")
	}
	if _, err := credential.LoadKeyring(r.Config.Credentials.KeyringFile); err != nil {
		if in.Mode == ModeLocal && errors.Is(err, os.ErrNotExist) {
			r.KeyringPending = true
		} else {
			return nil, fmt.Errorf("server.credentials.keyring_file: %w", err)
		}
	}
	if r.Bootstrap.Admin != nil {
		if strings.TrimSpace(r.Bootstrap.Admin.Username) == "" || strings.TrimSpace(r.Bootstrap.Admin.Email) == "" || r.Bootstrap.Admin.Password == "" {
			return nil, fmt.Errorf("bootstrap.admin requires username, email and password")
		}
	}
	r.describe(values, in.Mode)
	return r, nil
}

func (r *ResolvedServer) describe(values map[string]sourcedValue, mode string) {
	for _, f := range serverFields {
		source := "mode default"
		if v, ok := values[f.Key]; ok {
			source = v.source
		}
		if _, explicit := values[f.Key]; !explicit {
			switch f.Key {
			case "server.data_dir":
				if mode == ModeLocal {
					source = "local user configuration directory default"
				} else {
					source = "mode default (relative to working directory)"
				}
			case "server.credentials.keyring_file":
				source = "derived from data_dir"
			}
		}
		var value string
		if strings.HasPrefix(f.Key, "bootstrap.admin.") && r.Bootstrap.Admin == nil {
			value = "not supplied"
		} else {
			value = fmt.Sprint(f.read(r))
		}
		if f.Sensitive {
			value = "not configured"
			if strings.HasPrefix(f.Key, "bootstrap.admin.") {
				if r.Bootstrap.Admin != nil && r.Bootstrap.Admin.Password != "" {
					value = "configured (redacted)"
				}
			} else if s, ok := f.read(r).(string); ok && s != "" {
				value = "configured (redacted)"
			}
		}
		if f.Key == "server.database.dsn" && r.Config.Database.Driver == DatabaseDriverSQLite && r.Config.Database.DSN == "" {
			value = "derived from data_dir (redacted)"
		}
		if f.Key == "server.jwt_secret" && r.LocalSecretPath != "" {
			source = "local persistent instance key"
			if r.LocalSecretPending {
				value = "generated at startup"
			}
		}
		if f.Key == "server.credentials.keyring_file" && r.KeyringPending {
			value += " (generated for a new local instance at startup)"
		}
		if mode == ModeLocal && strings.HasPrefix(f.Key, "server.database.") {
			source = "local SQLite policy; database environment not applicable"
		}
		if mode == ModeLocal && strings.HasPrefix(f.Key, "bootstrap.admin.") {
			source = "not applicable"
		}
		if _, ok := values[f.Key]; !ok && (strings.HasPrefix(f.Key, "server.database.") || strings.HasSuffix(f.Key, ".queue_capacity") || f.Key == "server.sse.max_replay_events") {
			source = "derived default"
			if mode == ModeLocal && strings.HasPrefix(f.Key, "server.database.") {
				source = "local SQLite policy; database environment not applicable"
			}
		}
		r.Fields = append(r.Fields, FieldExplanation{f.FieldInfo, value, source})
	}
	if mode == ModeLocal {
		source := "command default"
		if r.AllowNetwork {
			source = "explicit flag: --allow-network; connecting clients have local administrator access"
		}
		r.Fields = append(r.Fields, FieldExplanation{FieldInfo: FieldInfo{Key: "command.allow_network", Type: "boolean", Modes: "local", Effect: "this process"}, Value: strconv.FormatBool(r.AllowNetwork), Source: source})
	}
}

func decodeServerDocument(data []byte, path string, env map[string]string, mode string, values map[string]sourcedValue) error {
	var doc yaml.Node
	d := yaml.NewDecoder(bytes.NewReader(data))
	if err := d.Decode(&doc); err != nil {
		return fmt.Errorf("deployment document contains invalid YAML")
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("deployment document must contain exactly one YAML document")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("deployment document must be an object")
	}
	root := doc.Content[0]
	seen := map[string]bool{}
	var kind, version *yaml.Node
	for i := 0; i < len(root.Content); i += 2 {
		key, node := root.Content[i], root.Content[i+1]
		if key.Tag != "!!str" {
			return fmt.Errorf("deployment document keys must be strings")
		}
		if seen[key.Value] {
			return fmt.Errorf("deployment document: duplicate key %s", key.Value)
		}
		seen[key.Value] = true
		switch key.Value {
		case "kind":
			kind = node
		case "version":
			version = node
		case "server", "log", "bootstrap":
			if err := decodeServerObject(node, key.Value, path, env, mode, values); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown deployment field %s", key.Value)
		}
	}
	if kind == nil || kind.Tag != "!!str" || kind.Value != "server" {
		return fmt.Errorf("deployment document requires kind: server")
	}
	if version == nil || version.Tag != "!!int" || version.Value != "1" {
		return fmt.Errorf("deployment document requires version: 1")
	}
	return nil
}

func decodeServerObject(node *yaml.Node, prefix, path string, env map[string]string, mode string, values map[string]sourcedValue) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("%s must be an object; null is not supported", prefix)
	}
	seen := map[string]bool{}
	for i := 0; i < len(node.Content); i += 2 {
		k, n := node.Content[i], node.Content[i+1]
		if k.Tag != "!!str" {
			return fmt.Errorf("%s keys must be strings", prefix)
		}
		key := prefix + "." + k.Value
		if seen[key] {
			return fmt.Errorf("duplicate field %s", key)
		}
		seen[key] = true
		f, ok := findServerField(key)
		if !ok {
			group := false
			for _, candidate := range serverFields {
				if strings.HasPrefix(candidate.Key, key+".") {
					group = true
					break
				}
			}
			if !group {
				return fmt.Errorf("unknown deployment field %s", key)
			}
			if mode == ModeLocal && (key == "server.database" || key == "bootstrap.admin") {
				return fmt.Errorf("%s is not applicable to local mode", key)
			}
			if err := decodeServerObject(n, key, path, env, mode, values); err != nil {
				return err
			}
			continue
		}
		if mode == ModeLocal && f.Modes == "serve" {
			return fmt.Errorf("%s is not applicable to local mode", key)
		}
		v, err := parseYAMLField(f, n, env)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		source := "file: " + path
		if f.Type == "path" && v.(string) != "" {
			v = absolutePath(v.(string), filepath.Dir(path))
			source += " (relative to document directory)"
		}
		values[key] = sourcedValue{v, source}
	}
	return nil
}

func parseYAMLField(f serverField, n *yaml.Node, env map[string]string) (any, error) {
	if n.Kind == yaml.AliasNode || n.Tag == "!!null" {
		return nil, fmt.Errorf("aliases and null are not supported")
	}
	switch f.Type {
	case "string", "path", "duration":
		if n.Kind != yaml.ScalarNode || n.Tag != "!!str" {
			return nil, fmt.Errorf("must be a string")
		}
		value, err := ExpandReferences(n.Value, env)
		if err != nil {
			return nil, err
		}
		if f.Type == "duration" {
			v, err := parseDuration(value)
			if err != nil {
				return nil, fmt.Errorf("must be a duration such as 30s or 5m")
			}
			return v, nil
		}
		return value, nil
	case "boolean":
		if n.Kind != yaml.ScalarNode || n.Tag != "!!bool" || (n.Value != "true" && n.Value != "false") {
			return nil, fmt.Errorf("must be true or false")
		}
		return n.Value == "true", nil
	case "integer":
		if n.Kind != yaml.ScalarNode || n.Tag != "!!int" {
			return nil, fmt.Errorf("must be an integer")
		}
		v, err := strconv.Atoi(n.Value)
		if err != nil {
			return nil, fmt.Errorf("must be a decimal integer")
		}
		return v, nil
	case "strings":
		if n.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("must be a string array")
		}
		values := make([]string, 0, len(n.Content))
		for _, child := range n.Content {
			if child.Kind != yaml.ScalarNode || child.Tag != "!!str" {
				return nil, fmt.Errorf("must contain only strings")
			}
			value, err := ExpandReferences(child.Value, env)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return values, nil
	}
	return nil, fmt.Errorf("unsupported input type")
}

// ExpandReferences expands scalar values after YAML decoding. It never alters
// document structure and reports missing references without including values.
func ExpandReferences(value string, env map[string]string) (string, error) {
	var missing string
	expanded := envVarPattern.ReplaceAllStringFunc(value, func(match string) string {
		parts := envVarPattern.FindStringSubmatch(match)
		v, present := env[parts[1]]
		if strings.Contains(match, ":-") && (!present || v == "") {
			return parts[2]
		}
		if !present {
			missing = parts[1]
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("environment reference %s is not set", missing)
	}
	return expanded, nil
}

func parseEnvironmentValue(f serverField, s string) (any, error) {
	switch f.Type {
	case "integer":
		v, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("must be an integer")
		}
		return v, nil
	case "duration":
		v, err := parseDuration(s)
		if err != nil {
			return nil, fmt.Errorf("must be a duration such as 30s or 5m")
		}
		return v, nil
	case "boolean":
		switch s {
		case "true", "1", "yes":
			return true, nil
		case "false", "0", "no":
			return false, nil
		default:
			return nil, fmt.Errorf("must be true/false/1/0/yes/no")
		}
	case "strings":
		if s == "" {
			return []string{}, nil
		}
		values := strings.Split(s, ",")
		for i := range values {
			values[i] = strings.TrimSpace(values[i])
		}
		return values, nil
	default:
		return s, nil
	}
}

func parseDuration(value string) (time.Duration, error) {
	if strings.Trim(value, "+-0123456789") == "" {
		return 0, errors.New("duration requires a unit")
	}
	return time.ParseDuration(value)
}

func validValueType(kind string, v any) bool {
	switch kind {
	case "string", "path":
		_, ok := v.(string)
		return ok
	case "integer":
		_, ok := v.(int)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "duration":
		_, ok := v.(time.Duration)
		return ok
	case "strings":
		_, ok := v.([]string)
		return ok
	}
	return false
}
func findServerField(key string) (serverField, bool) {
	for _, f := range serverFields {
		if f.Key == key {
			return f, true
		}
	}
	return serverField{}, false
}
func absolutePath(path, base string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	return filepath.Clean(path)
}
func removeFinalNewline(value string) string {
	if strings.HasSuffix(value, "\r\n") {
		return strings.TrimSuffix(value, "\r\n")
	}
	return strings.TrimSuffix(value, "\n")
}
func flagField(key string) bool {
	switch key {
	case "server.host", "server.port", "server.data_dir", "server.auto_migrate", "server.serve_ui", "log.level", "log.format":
		return true
	}
	return false
}
func flagName(key string) string {
	switch key {
	case "server.data_dir":
		return "data-dir"
	case "server.auto_migrate":
		return "auto-migrate"
	case "server.serve_ui":
		return "no-ui"
	case "log.level":
		return "log-level"
	case "log.format":
		return "log-format"
	}
	return strings.TrimPrefix(key, "server.")
}

func validateLocalHost(host string, allowNetwork bool) error {
	if allowNetwork {
		return nil
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("server.host: local non-loopback listening requires explicit --allow-network; connecting clients will have local administrator access")
	}
	return nil
}

// ReadLocalSecret reads an existing persistent secret; absence is distinguished
// from unreadability or corruption so startup can safely decide whether to create.
func ReadLocalSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		return "", fmt.Errorf("server.jwt_secret: cannot read local instance key")
	}
	value := removeFinalNewline(string(b))
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return "", fmt.Errorf("server.jwt_secret: local instance key is malformed")
	}
	return value, nil
}
