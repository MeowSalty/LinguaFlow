package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

func deploymentInputs(t *testing.T) ServerInputs {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "keyring.json")
	document := fmt.Sprintf("{\"version\":1,\"active_key_id\":\"test\",\"keys\":{\"test\":\"%s\"}}", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	if _, err := credential.PublishPrivateFile(path, []byte(document)); err != nil {
		t.Fatal(err)
	}
	return ServerInputs{Mode: ModeServer, WorkingDirectory: dir, UserConfigDir: dir, Environment: map[string]string{"LINGUAFLOW_JWT_SECRET": strings.Repeat("s", 32), "LINGUAFLOW_CREDENTIALS_KEYRING_FILE": path}}
}
func withDocument(t *testing.T, in *ServerInputs, document string) {
	t.Helper()
	path := filepath.Join(in.WorkingDirectory, "server.yaml")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	in.ConfigPath = &path
}
func explainField(t *testing.T, r *ResolvedServer, key string) FieldExplanation {
	t.Helper()
	for _, f := range r.Fields {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("missing field %s", key)
	return FieldExplanation{}
}

func TestDeploymentPrecedenceAndPresence(t *testing.T) {
	in := deploymentInputs(t)
	withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  host: 127.0.0.1\n  port: 8001\n  serve_ui: true\n  database:\n    max_open_conns: 0\n    max_idle_conns: 0\n  pipeline:\n    rss_limit_mb: 0\n  cors:\n    allowed_origins: []\n")
	in.Environment["LINGUAFLOW_PORT"] = "8002"
	in.Environment["LINGUAFLOW_SERVE_UI"] = "false"
	in.Overrides = map[string]any{"server.port": 8003, "server.auto_migrate": false}
	r, err := ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Port != 8003 || r.Config.ServeUI || r.Config.AutoMigrate {
		t.Fatalf("explicit inputs were lost: %+v", r.Config)
	}
	if r.Config.Database.MaxOpenConns != 0 || r.Config.Database.MaxIdleConns != 0 || len(r.Config.CORS.AllowedOrigins) != 0 {
		t.Fatal("explicit zero or empty list was lost")
	}
	if got := explainField(t, r, "server.port"); got.Source != "flag: --port" {
		t.Fatalf("source=%s", got.Source)
	}
	in.Overrides = nil
	r, err = ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Port != 8002 {
		t.Fatal("unset flag swallowed environment")
	}
}

func TestDeploymentDerivedDefaults(t *testing.T) {
	in := deploymentInputs(t)
	in.Environment["LINGUAFLOW_DATABASE_DRIVER"] = "postgres"
	in.Environment["LINGUAFLOW_DATABASE_DSN"] = "postgres://example/linguaflow"
	in.Environment["LINGUAFLOW_WORKERS_TRANSLATION_COUNT"] = "3"
	in.Environment["LINGUAFLOW_WORKERS_SYNC_COUNT"] = "4"
	in.Environment["LINGUAFLOW_SSE_RING_BUFFER_CAPACITY"] = "12"
	r, err := ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Database.MaxOpenConns != 25 || r.Config.Database.MaxIdleConns != 5 || r.Config.Database.ConnMaxLifetime != 30*time.Minute {
		t.Fatalf("postgres defaults: %+v", r.Config.Database)
	}
	if r.Config.Workers.Translation.QueueCapacity != 12 || r.Config.Workers.Sync.QueueCapacity != 32 || r.Config.SSE.MaxReplayEvents != 24 {
		t.Fatal("derived defaults did not use final inputs")
	}
	in.Environment["LINGUAFLOW_WORKERS_TRANSLATION_QUEUE_CAPACITY"] = "1"
	in.Environment["LINGUAFLOW_SSE_MAX_REPLAY_EVENTS"] = "1"
	in.Environment["LINGUAFLOW_DATABASE_MAX_OPEN_CONNS"] = "0"
	r, err = ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Workers.Translation.QueueCapacity != 1 || r.Config.SSE.MaxReplayEvents != 1 || r.Config.Database.MaxOpenConns != 0 {
		t.Fatal("derived defaults overwrote explicit values")
	}
}

func TestDeploymentRejectsMalformedSources(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"kind", "kind: translation\nversion: 1\n", "kind"},
		{"missing version", "kind: server\n", "version"},
		{"version", "kind: server\nversion: 2\n", "version"},
		{"quoted version", "kind: server\nversion: '1'\n", "version"},
		{"unknown", "kind: server\nversion: 1\nserver:\n  unused: true\n", "unknown"},
		{"mode", "kind: server\nversion: 1\nserver:\n  mode: local\n", "unknown"},
		{"duplicate", "kind: server\nversion: 1\nserver:\n  port: 1\n  port: 2\n", "duplicate"},
		{"duplicate group", "kind: server\nversion: 1\nserver: {}\nserver: {}\n", "duplicate"},
		{"null", "kind: server\nversion: 1\nserver:\n  port: null\n", "null"},
		{"null group", "kind: server\nversion: 1\nserver: null\n", "object"},
		{"string bool", "kind: server\nversion: 1\nserver:\n  serve_ui: 'false'\n", "true or false"},
		{"yaml yes", "kind: server\nversion: 1\nserver:\n  serve_ui: yes\n", "true or false"},
		{"duration number", "kind: server\nversion: 1\nserver:\n  shutdown_timeout: 30\n", "string"},
		{"multiple documents", "kind: server\nversion: 1\n---\nkind: server\nversion: 1\n", "exactly one"},
		{"alias", "kind: server\nversion: 1\nserver:\n  host: &host 127.0.0.1\n  jwt_issuer: *host\n", "aliases"},
		{"old registration", "kind: server\nversion: 1\nserver:\n  registration:\n    enabled: true\n", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := deploymentInputs(t)
			withDocument(t, &in, tt.body)
			_, err := ResolveServerConfig(in)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v want %s", err, tt.want)
			}
		})
	}
}

func TestDeploymentRejectsInvalidValuesWithoutRepair(t *testing.T) {
	cases := map[string]string{
		"LINGUAFLOW_PORT": "0", "LINGUAFLOW_PREVIEW_TIMEOUT": "0s", "LINGUAFLOW_QUICK_TRANSLATE_MAX_CONCURRENCY": "33",
		"LINGUAFLOW_QUICK_TRANSLATE_MAX_TIMEOUT": "31m", "LINGUAFLOW_QUICK_TRANSLATE_TIMEOUT": "31m",
		"LINGUAFLOW_WORKERS_TRANSLATION_COUNT": "0", "LINGUAFLOW_PIPELINE_RSS_LIMIT_MB": "-1",
		"LINGUAFLOW_SERVE_UI": "typo", "LINGUAFLOW_LOG_LEVEL": "verbose", "LINGUAFLOW_LOG_FORMAT": "xml",
		"LINGUAFLOW_DATABASE_MAX_OPEN_CONNS": "-1", "LINGUAFLOW_DATABASE_MAX_IDLE_CONNS": "-1",
		"LINGUAFLOW_DATABASE_CONN_MAX_LIFETIME": "-1s", "LINGUAFLOW_DATABASE_DSN": "relative.db",
		"LINGUAFLOW_JWT_SECRET": "short", "LINGUAFLOW_DATA_DIR": "", "LINGUAFLOW_JWT_EXPIRY": "15",
	}
	for key, value := range cases {
		t.Run(key, func(t *testing.T) {
			in := deploymentInputs(t)
			in.Environment[key] = value
			if _, err := ResolveServerConfig(in); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
	in := deploymentInputs(t)
	in.Environment["LINGUAFLOW_PORT"] = "broken"
	in.Overrides = map[string]any{"server.port": 8080}
	if _, err := ResolveServerConfig(in); err == nil {
		t.Fatal("overridden invalid environment accepted")
	}
	in = deploymentInputs(t)
	withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  port: broken\n")
	in.Environment["LINGUAFLOW_PORT"] = "8080"
	if _, err := ResolveServerConfig(in); err == nil {
		t.Fatal("overridden invalid YAML accepted")
	}
}

func TestDeploymentStrictBooleans(t *testing.T) {
	for _, value := range []string{"true", "false", "1", "0", "yes", "no"} {
		t.Run(value, func(t *testing.T) {
			in := deploymentInputs(t)
			in.Environment["LINGUAFLOW_SERVE_UI"] = value
			r, err := ResolveServerConfig(in)
			if err != nil {
				t.Fatal(err)
			}
			want := value == "true" || value == "1" || value == "yes"
			if r.Config.ServeUI != want {
				t.Fatal("wrong boolean value")
			}
		})
	}
	for _, value := range []string{"", "TRUE", "False", "maybe"} {
		in := deploymentInputs(t)
		in.Environment["LINGUAFLOW_SERVE_UI"] = value
		if _, err := ResolveServerConfig(in); err == nil {
			t.Fatalf("accepted boolean %q", value)
		}
	}
}

func TestDeploymentReferencesDoNotChangeYAML(t *testing.T) {
	in := deploymentInputs(t)
	secret := "private: value\nserver:\n  port: 9\n\"quoted\""
	delete(in.Environment, "LINGUAFLOW_JWT_SECRET")
	in.Environment["PRIVATE_KEY"] = secret
	withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  jwt_secret: ${PRIVATE_KEY}\n  port: 8081\n")
	r, err := ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.JWTSecret != secret || r.Config.Port != 8081 {
		t.Fatal("substitution changed scalar or document")
	}
	if strings.Contains(fmt.Sprint(r.Fields), secret) {
		t.Fatal("explanation exposed secret")
	}
	delete(in.Environment, "PRIVATE_KEY")
	_, err = ResolveServerConfig(in)
	if err == nil || !strings.Contains(err.Error(), "PRIVATE_KEY") {
		t.Fatalf("missing reference: %v", err)
	}
	if value, err := ExpandReferences("${EMPTY:-fallback}", map[string]string{"EMPTY": ""}); err != nil || value != "fallback" {
		t.Fatalf("empty fallback=%s err=%v", value, err)
	}
	if value, err := ExpandReferences("${EMPTY}", map[string]string{"EMPTY": ""}); err != nil || value != "" {
		t.Fatalf("explicit empty=%s err=%v", value, err)
	}
}

func TestDeploymentSecretFiles(t *testing.T) {
	in := deploymentInputs(t)
	delete(in.Environment, "LINGUAFLOW_JWT_SECRET")
	path := filepath.Join(in.WorkingDirectory, "jwt")
	secret := strings.Repeat("x", 32) + " "
	if err := os.WriteFile(path, []byte(secret+"\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in.Environment["LINGUAFLOW_JWT_SECRET_FILE"] = "jwt"
	r, err := ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.JWTSecret != secret {
		t.Fatal("secret whitespace was trimmed")
	}
	in.Environment["LINGUAFLOW_JWT_SECRET"] = "other"
	_, err = ResolveServerConfig(in)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflict=%v", err)
	}
	delete(in.Environment, "LINGUAFLOW_JWT_SECRET")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveServerConfig(in); err == nil {
		t.Fatal("empty secret file accepted")
	}
	if got := removeFinalNewline("key\r"); got != "key\r" {
		t.Fatal("stripped non-newline CR")
	}
}

func TestDeploymentPathBasesAndSelection(t *testing.T) {
	in := deploymentInputs(t)
	dir := filepath.Join(in.WorkingDirectory, "documents")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(path, []byte("kind: server\nversion: 1\nserver:\n  data_dir: relative-data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	in.Environment["LINGUAFLOW_SERVER_CONFIG"] = filepath.Join("documents", "server.yaml")
	r, err := ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.DataDir != filepath.Join(dir, "relative-data") {
		t.Fatalf("file path=%s", r.Config.DataDir)
	}
	in.Environment["LINGUAFLOW_DATA_DIR"] = "env-data"
	r, err = ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.DataDir != filepath.Join(in.WorkingDirectory, "env-data") {
		t.Fatalf("env path=%s", r.Config.DataDir)
	}
	missing := "missing.yaml"
	in.ConfigPath = &missing
	if _, err := ResolveServerConfig(in); err == nil {
		t.Fatal("explicit missing file fell back")
	}
	empty := ""
	in.ConfigPath = &empty
	if _, err := ResolveServerConfig(in); err == nil {
		t.Fatal("explicit empty path fell back")
	}
}

func TestLocalResolutionIsOfflineAndRespectsInputs(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "not-created")
	in := ServerInputs{Mode: ModeLocal, WorkingDirectory: dir, UserConfigDir: dir, Environment: map[string]string{
		"LINGUAFLOW_DATA_DIR": dataDir, "LINGUAFLOW_PORT": "0", "LINGUAFLOW_DATABASE_DRIVER": "postgres", "LINGUAFLOW_DATABASE_DSN": "broken", "LINGUAFLOW_DATABASE_MAX_OPEN_CONNS": "broken",
	}}
	r, err := ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Host != "127.0.0.1" || r.Config.Port != 0 || r.Config.DataDir != dataDir || r.Config.Database.Driver != DatabaseDriverSQLite {
		t.Fatalf("local mode inputs: %+v", r.Config)
	}
	if !r.LocalSecretPending || !r.KeyringPending || r.Config.JWTSecret != "" {
		t.Fatal("missing secret was made into a runtime value")
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("offline resolution created directory: %v", err)
	}
	if !strings.Contains(explainField(t, r, "server.jwt_secret").Value, "startup") {
		t.Fatal("missing pending key explanation")
	}
	if !strings.Contains(explainField(t, r, "server.database.driver").Source, "not applicable") {
		t.Fatal("missing ignored env explanation")
	}
}

func TestLocalNetworkPermission(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "127.1.2.3", "::1", "localhost"} {
		dir := t.TempDir()
		_, err := ResolveServerConfig(ServerInputs{Mode: ModeLocal, WorkingDirectory: dir, UserConfigDir: dir, Environment: map[string]string{"LINGUAFLOW_HOST": host}})
		if err != nil {
			t.Fatalf("loopback %s: %v", host, err)
		}
	}
	for _, host := range []string{"0.0.0.0", "::", "192.0.2.1", "example.com"} {
		dir := t.TempDir()
		in := ServerInputs{Mode: ModeLocal, WorkingDirectory: dir, UserConfigDir: dir, Environment: map[string]string{"LINGUAFLOW_HOST": host}}
		if _, err := ResolveServerConfig(in); err == nil {
			t.Fatalf("host %s accepted without permission", host)
		}
		in.AllowNetwork = true
		if _, err := ResolveServerConfig(in); err != nil {
			t.Fatalf("explicit permission: %v", err)
		}
	}
	dir := t.TempDir()
	r, err := ResolveServerConfig(ServerInputs{Mode: ModeLocal, WorkingDirectory: dir, UserConfigDir: dir, AllowNetwork: true})
	if err != nil || r.Config.Host != "127.0.0.1" {
		t.Fatalf("permission changed host: %v", err)
	}
}

func TestLocalRejectsInapplicableFileFields(t *testing.T) {
	for _, body := range []string{"server:\n  database:\n    driver: sqlite\n", "bootstrap:\n  admin: {}\n", "server:\n  allow_network: true\n"} {
		in := deploymentInputs(t)
		in.Mode = ModeLocal
		in.Environment = map[string]string{}
		withDocument(t, &in, "kind: server\nversion: 1\n"+body)
		if _, err := ResolveServerConfig(in); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
}

func TestValidationDoesNotMutate(t *testing.T) {
	in := deploymentInputs(t)
	r, err := ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	r.Config.QuickTranslate.MaxConcurrency = 100
	before := r.Config
	if err := ValidateServerConfig(&r.Config); err == nil {
		t.Fatal("invalid limit accepted")
	}
	if !reflect.DeepEqual(before, r.Config) {
		t.Fatal("validation mutated configuration")
	}
}

func TestFinalSemanticValidationAllowsOverriddenValues(t *testing.T) {
	in := deploymentInputs(t)
	withDocument(t, &in, "kind: server\nversion: 1\nserver:\n  data_dir: ''\n  port: -1\n")
	in.Environment["LINGUAFLOW_DATA_DIR"] = "final-data"
	in.Environment["LINGUAFLOW_PORT"] = "8082"
	r, err := ResolveServerConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Config.Port != 8082 || r.Config.DataDir != filepath.Join(in.WorkingDirectory, "final-data") {
		t.Fatal("higher source was not applied before semantic validation")
	}
}

func TestNoImplicitDocumentSearch(t *testing.T) {
	in := deploymentInputs(t)
	if err := os.WriteFile(filepath.Join(in.WorkingDirectory, "server.yaml"), []byte("broken: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveServerConfig(in); err != nil {
		t.Fatalf("implicitly searched working directory: %v", err)
	}
}

func TestDurationRequiresUnitsEvenForZero(t *testing.T) {
	in := deploymentInputs(t)
	in.Environment["LINGUAFLOW_DATABASE_CONN_MAX_LIFETIME"] = "0"
	if _, err := ResolveServerConfig(in); err == nil {
		t.Fatal("bare duration zero accepted")
	}
	in.Environment["LINGUAFLOW_DATABASE_CONN_MAX_LIFETIME"] = "0s"
	if _, err := ResolveServerConfig(in); err != nil {
		t.Fatal(err)
	}
}

func TestKeyringErrorsAreRedacted(t *testing.T) {
	in := deploymentInputs(t)
	const private = "do-not-print-this-keyring-content"
	if err := os.WriteFile(in.Environment["LINGUAFLOW_CREDENTIALS_KEYRING_FILE"], []byte(private), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveServerConfig(in)
	if err == nil || strings.Contains(err.Error(), private) {
		t.Fatalf("unsafe keyring error: %v", err)
	}
	in.Environment["LINGUAFLOW_CREDENTIALS_KEYRING_FILE"] = filepath.Join(in.WorkingDirectory, "missing-keyring")
	if _, err := ResolveServerConfig(in); err == nil {
		t.Fatal("serve accepted missing keyring")
	}
}

func TestPublicDeploymentFieldsAreUnique(t *testing.T) {
	keys, envs := map[string]bool{}, map[string]bool{}
	for _, f := range DeploymentFields() {
		if keys[f.Key] || envs[f.Environment] {
			t.Fatalf("duplicate field %s", f.Key)
		}
		keys[f.Key] = true
		envs[f.Environment] = true
		if f.Type == "" || f.Modes == "" || f.Effect == "" {
			t.Fatalf("incomplete field contract: %+v", f)
		}
	}
	if len(keys) < 40 {
		t.Fatal("missing deployment field coverage")
	}
}

func TestDatabaseDSN_CustomSQLiteForcesForeignKeys(t *testing.T) {
	cfg := DefaultServerConfig()
	cfg.Database.DSN = filepath.Join(t.TempDir(), "custom.db") + "?_pragma=foreign_keys(0)"
	if !strings.HasSuffix(cfg.DatabaseDSN(), "&_pragma=foreign_keys(1)") {
		t.Fatal("foreign key pragma missing")
	}
}
