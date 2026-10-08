package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	"github.com/MeowSalty/LinguaFlow/backend/internal/templates"
)

const minimalTranslation = `kind: translation
version: 2
source_lang: en
target_lang: zh
backends:
  test:
    type: openai
    secret: ${KEY}
    options:
      model: test-model
execution:
  rounds:
    - mode: translate
      backend: test
      translate: {}
`

func translationInput(t *testing.T, document string) CLIInputs {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "translation.yaml")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return CLIInputs{ConfigPath: &path, WorkingDirectory: dir, Environment: map[string]string{"KEY": "private-key"}}
}
func TestTranslationBuiltinsUseStrictReferencePipeline(t *testing.T) {
	in := CLIInputs{WorkingDirectory: t.TempDir(), Environment: map[string]string{"OPENAI_API_KEY": "test-secret"}}
	cfg, err := ResolveCLIConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Kind != "translation" || cfg.Version != 2 || cfg.Backends["openai-default"].Secret != "test-secret" {
		t.Fatal("builtin pipeline did not resolve new contract")
	}
	profile, err := ResolveExecutionProfile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(profile, execution.DefaultProfile()) {
		t.Fatalf("builtin profile diverges from domain defaults: %+v", profile)
	}
	if cfg.PromptTemplates["通用提示词"].File != "" || cfg.PromptTemplates["通用提示词"].Content == "" || cfg.BootstrapPromptTemplates["通用术语抽取"].Content == "" {
		t.Fatal("builtin references were not materialized")
	}
	delete(in.Environment, "OPENAI_API_KEY")
	if _, err := ResolveCLIConfig(in); err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("missing key was ignored: %v", err)
	}
}
func TestGeneratedAndBuiltinTranslationHaveSameValues(t *testing.T) {
	dir := t.TempDir()
	err := fs.WalkDir(templates.EmbeddedFS(), "default", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if path != "default/linguaflow.yaml" && path != "default/profiles/default.yaml" && !strings.HasPrefix(path, "default/prompts/") {
			return nil
		}
		target := filepath.Join(dir, strings.TrimPrefix(path, "default/"))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		data, err := fs.ReadFile(templates.EmbeddedFS(), path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
	in := CLIInputs{WorkingDirectory: dir, Environment: map[string]string{"OPENAI_API_KEY": "test-secret"}}
	builtin, err := ResolveCLIConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "linguaflow.yaml")
	in.ConfigPath = &path
	file, err := ResolveCLIConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(builtin, file) {
		t.Fatal("generated document differs from builtins")
	}
}
func TestTranslationPresenceAndDefaults(t *testing.T) {
	in := translationInput(t, minimalTranslation+`translation_profiles:
  custom:
    schema_version: 1
    context:
      enabled: false
      before: 0
      after: 0
    ruby:
      enabled: false
      preserve_kinds: []
`)
	cfg, err := ResolveCLIConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.TranslationProfiles["custom"]
	if p.Context.Enabled || p.Context.Before != 0 || p.Context.After != 0 || p.Ruby.Enabled || p.Ruby.PreserveKinds == nil || len(p.Ruby.PreserveKinds) != 0 {
		t.Fatalf("explicit zero values lost: %+v", p)
	}
	if !p.Protect.Enabled || !p.Repair.PromptUpgrade {
		t.Fatal("omitted fields did not use domain defaults")
	}
	tr := cfg.Execution.Rounds[0].Translate
	if tr.BatchSize != 1 || tr.Concurrency != 4 || tr.Retry.MaxAttempts != 3 || !cfg.Backends["test"].Enabled {
		t.Fatal("typed object defaults missing")
	}
	in = translationInput(t, strings.Replace(minimalTranslation, "translate: {}", "translate:\n        retry:\n          max_attempts: 0\n          jitter: false", 1))
	cfg, err = ResolveCLIConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Execution.Rounds[0].Translate.Retry.MaxAttempts != 0 || cfg.Execution.Rounds[0].Translate.Retry.Jitter {
		t.Fatal("explicit retry zero/false overwritten")
	}
}
func TestTranslationStrictSchema(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"old", strings.Replace(minimalTranslation, "version: 2", "version: 1", 1), "version"},
		{"missing version", strings.Replace(minimalTranslation, "version: 2\n", "", 1), "version"},
		{"wrong kind", strings.Replace(minimalTranslation, "kind: translation", "kind: server", 1), "kind"},
		{"unknown", minimalTranslation + "unknown: true\n", "unknown"},
		{"duplicate", minimalTranslation + "version: 2\n", "duplicate"},
		{"null", strings.Replace(minimalTranslation, "translate: {}", "translate: null", 1), "null"},
		{"bool", strings.Replace(minimalTranslation, "type: openai", "type: openai\n    enabled: yes", 1), "true or false"},
		{"number to string", strings.Replace(minimalTranslation, "target_lang: zh", "target_lang: 1", 1), "string"},
		{"unrecognized mode", strings.Replace(minimalTranslation, "mode: translate", "mode: correct", 1), "supports"},
		{"missing mode", strings.Replace(minimalTranslation, "- mode: translate", "- backend: test", 1), "duplicate"},
		{"two documents", minimalTranslation + "---\nkind: translation\n", "exactly one"},
		{"legacy secret", strings.Replace(minimalTranslation, "model: test-model", "model: test-model\n      api_key: private", 1), "api_key"},
		{"unknown option", strings.Replace(minimalTranslation, "model: test-model", "model: test-model\n      unknown: true", 1), "unsupported"},
		{"zero concurrency", strings.Replace(minimalTranslation, "translate: {}", "translate:\n        concurrency: 0", 1), "invalid"},
		{"missing profile version", minimalTranslation + "translation_profiles:\n  custom:\n    context:\n      enabled: false\n", "schema_version"},
		{"old bootstrap", minimalTranslation + "translation_profiles:\n  custom:\n    schema_version: 1\n    bootstrap:\n      enabled: true\n", "unknown"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveCLIConfig(translationInput(t, tt.doc))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v want=%s", err, tt.want)
			}
		})
	}
}
func TestUnsupportedCLICapabilitiesAreErrors(t *testing.T) {
	for _, extra := range []string{"translation_memory:\n  enabled: true\n", "translation_memory:\n  driver: sqlite\n", "plugins:\n  scripts: [x.lua]\n", "output:\n  mode: overwrite\n", "translation_profiles:\n  custom:\n    schema_version: 1\n    qa:\n      enabled: true\n"} {
		if _, err := ResolveCLIConfig(translationInput(t, minimalTranslation+extra)); err == nil {
			t.Fatalf("unsupported setting accepted: %s", extra)
		}
	}
}
func TestTranslationReferencesAreScalarAndPromptBodiesAreLiteral(t *testing.T) {
	in := translationInput(t, minimalTranslation+`translation_prompt_templates:
  literal:
    content: |
      Preserve ${NOT_AN_ENVIRONMENT_VARIABLE}.
`)
	secret := "private: value\nserver:\n  port: 9\n\"quote\""
	in.Environment["KEY"] = secret
	cfg, err := ResolveCLIConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Backends["test"].Secret != secret || !strings.Contains(cfg.PromptTemplates["literal"].Content, "${NOT_AN_ENVIRONMENT_VARIABLE}") {
		t.Fatal("scalar or prompt body changed")
	}
	delete(in.Environment, "KEY")
	if _, err := ResolveCLIConfig(in); err == nil || !strings.Contains(err.Error(), "KEY") {
		t.Fatalf("missing reference=%v", err)
	}
}
func TestExternalProfileUsesDomainDefaultsAndStrictSchema(t *testing.T) {
	in := translationInput(t, minimalTranslation+"translation_profiles:\n  custom:\n    file: profile.yaml\n")
	file := filepath.Join(in.WorkingDirectory, "profile.yaml")
	if err := os.WriteFile(file, []byte("schema_version: 1\ncontext:\n  enabled: false\n  before: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveCLIConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.TranslationProfiles["custom"]
	if p.Context.Enabled || p.Context.Before != 0 || p.Context.After != 1 || !p.Protect.Enabled {
		t.Fatalf("external defaults/presence=%+v", p)
	}
	if err := os.WriteFile(file, []byte("schema_version: 1\nrepair:\n  partial: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCLIConfig(in); err == nil {
		t.Fatal("ignored obsolete external field")
	}
}
func TestReferencesRejectMixingAndEscape(t *testing.T) {
	for _, extra := range []string{
		"translation_prompt_templates:\n  custom:\n    content: body\n    file: ''\n",
		"translation_profiles:\n  custom:\n    file: profile.yaml\n    schema_version: 1\n",
	} {
		if _, err := ResolveCLIConfig(translationInput(t, minimalTranslation+extra)); err == nil {
			t.Fatal("mixed reference accepted")
		}
	}
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	base := filepath.Join(dir, "config")
	if err := os.Mkdir(base, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readExternalFileBytes("../outside", base); err == nil {
		t.Fatal("directory escape accepted")
	}
	if _, err := readExternalFileBytes(outside, base); err == nil {
		t.Fatal("absolute reference accepted")
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := readExternalFileBytes("link", base); err == nil {
		t.Fatal("symlink escape accepted")
	}
}
func TestTranslationPathSelectionAndFinalOverrides(t *testing.T) {
	in := translationInput(t, minimalTranslation)
	path := *in.ConfigPath
	in.ConfigPath = nil
	in.Environment["LINGUAFLOW_TRANSLATION_CONFIG"] = "translation.yaml"
	cfg, err := ResolveCLIConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Glossary.Path != filepath.Join(in.WorkingDirectory, "glossary.csv") {
		t.Fatalf("document path base=%s", cfg.Glossary.Path)
	}
	missing := "missing.yaml"
	in.ConfigPath = &missing
	if _, err := ResolveCLIConfig(in); !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("missing path fell back: %v", err)
	}
	in.ConfigPath = &path
	from, level := "ja", "debug"
	in.SourceLang = &from
	in.LogLevel = &level
	cfg, err = ResolveCLIConfig(in)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SourceLang != "ja" || cfg.Log.Level != "debug" {
		t.Fatal("final input override missing")
	}
	empty := ""
	in.ConfigPath = &empty
	if _, err := ResolveCLIConfig(in); err == nil {
		t.Fatal("explicit empty config path accepted")
	}
}
func TestUnknownProfileDoesNotFallback(t *testing.T) {
	cfg, err := ResolveCLIConfig(translationInput(t, minimalTranslation))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Execution.Profile = "misspelled"
	if _, err := ResolveExecutionProfile(cfg); err == nil {
		t.Fatal("unknown profile silently fell back")
	}
}

func TestExplicitEmptyTranslationReferencesAreRejected(t *testing.T) {
	for name, document := range map[string]string{
		"profile": strings.Replace(minimalTranslation, "execution:\n", "execution:\n  profile: ''\n", 1),
		"prompt":  strings.Replace(minimalTranslation, "translate: {}", "translate: {prompt: ''}", 1),
		"extract": minimalTranslation + "    - mode: extract\n      backend: test\n      extract: {template: ''}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ResolveCLIConfig(translationInput(t, document)); err == nil {
				t.Fatal("explicit empty reference silently used defaults")
			}
		})
	}
}
