package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGoFunctionBodiesDistinguishPredicateTypes(t *testing.T) {
	tests := []struct {
		name   string
		source string
		body   bool
	}{
		{"predicate", "package p; type Predicate func(int)", false},
		{"alias", "package p; type Callback = func() func(int) error", false},
		{"generic alias", "package p; type Callback[T any] = func(T) T", false},
		{"nested types", "package p; type Callbacks struct { F func() interface { Call(func()) func() } }", false},
		{"bodyless declaration", "package p; func Assembly(func()) func()", false},
		{"function conversion", "package p; var callback = (func())(nil)", false},
		{"comments and literals", "package p; /* func F() {} */ const s = `func() {}`; const r = '{'", false},
		{"empty function", "package p; func F() {}", true},
		{"method", "package p; type T int; func (T) Call() {}", true},
		{"generic function", "package p; func F[T any](v T) T { return v }", true},
		{"literal", "package p; var callback = func() {}", true},
		{"returning function type", "package p; var callback = func() func() { return nil }", true},
		{"nested literal", "package p; var callbacks = []func(){func() { println(1) }}", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := hasGoFunctionBody("fixture.go", []byte(tt.source))
			if err != nil {
				t.Fatal(err)
			}
			if body != tt.body {
				t.Fatalf("function body=%v, want %v", body, tt.body)
			}
		})
	}
	// This is the actual generated package that the hosted coverage run rejected.
	directory, err := filepath.Abs("../../ent/predicate")
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := noFunctionSourceEvidence(packageRecord{Dir: directory, GoFiles: []string{"predicate.go"}})
	if err != nil || evidence == nil {
		t.Fatalf("generated predicate coverage evidence=%+v, err=%v", evidence, err)
	}
}

func TestSourceEvidenceUsesAllAndOnlySelectedProductionFiles(t *testing.T) {
	directory := t.TempDir()
	sources := map[string]string{
		"types.go": "package p; type Predicate func(int)",
		"cgo.go":   "package p; type Callback = func()",
		"other.go": "package p; func Unselected() {}",
	}
	for name, source := range sources {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	record := packageRecord{Dir: directory, GoFiles: []string{"types.go"}, CgoFiles: []string{"cgo.go"}}
	evidence, err := noFunctionSourceEvidence(record)
	if err != nil || evidence == nil || len(evidence.Files) != 2 {
		t.Fatalf("selected files evidence=%+v, err=%v", evidence, err)
	}
	for _, file := range evidence.Files {
		digest := sha256.Sum256([]byte(sources[file.Name]))
		if file.SHA256 != hex.EncodeToString(digest[:]) {
			t.Fatalf("wrong selected file digest: %+v", file)
		}
	}
	record.GoFiles = append(record.GoFiles, "other.go")
	if evidence, err := noFunctionSourceEvidence(record); err != nil || evidence != nil {
		t.Fatalf("executable file acquired exemption=%+v, err=%v", evidence, err)
	}
	record.TestGoFiles = []string{"types_test.go"}
	if evidence, err := noFunctionSourceEvidence(record); err != nil || evidence != nil {
		t.Fatalf("tested package acquired exemption=%+v, err=%v", evidence, err)
	}
}

func TestSourceEvidenceRejectsInvalidSelectedSources(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "valid.go"), []byte("package p; func Work() {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "invalid.go"), []byte("package p; type Callback func("), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, names := range [][]string{
		{"invalid.go"}, {"valid.go", "invalid.go"}, {"missing.go"}, {"../valid.go"}, {`..\valid.go`}, {"valid.go", "valid.go"},
	} {
		if _, err := noFunctionSourceEvidence(packageRecord{Dir: directory, GoFiles: names}); err == nil {
			t.Fatalf("accepted invalid selected source list: %v", names)
		}
	}
}

func TestInventoryPreservesPackageMetadataAndRejectsIncompleteDiscovery(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "types.go"), []byte("package p; type Predicate func(int)"), 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]any{
		"ImportPath": "fixture/predicate", "Dir": directory, "GoFiles": []string{"types.go"},
		"Deps": []string{"fixture/sql"}, "Module": map[string]string{"Path": "fixture"},
	})
	if err != nil {
		t.Fatal(err)
	}
	records, err := annotateInventory(input)
	if err != nil || len(records) != 1 {
		t.Fatalf("inventory=%v, err=%v", records, err)
	}
	if string(records[0]["Deps"]) != `["fixture/sql"]` || string(records[0]["Module"]) != `{"Path":"fixture"}` {
		t.Fatalf("discovered metadata changed: %v", records[0])
	}
	var evidence sourceEvidence
	if err := json.Unmarshal(records[0]["NoFunctionSourceEvidence"], &evidence); err != nil || evidence.Kind != "no-go-function-bodies" || evidence.Parser != "go/parser" {
		t.Fatalf("inventory AST evidence=%+v, err=%v", evidence, err)
	}
	for _, data := range [][]byte{
		nil, []byte(`{"ImportPath":"broken","Incomplete":true}`), []byte(`{"ImportPath":"broken","Error":{"Err":"missing dependency"}}`),
		append(append([]byte(nil), input...), input...), append(append([]byte(nil), input...), []byte(`{`)...),
	} {
		if _, err := annotateInventory(data); err == nil {
			t.Fatalf("accepted incomplete or repeated discovery: %s", data)
		}
	}
}
