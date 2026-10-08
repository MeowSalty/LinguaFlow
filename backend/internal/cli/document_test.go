package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicOutputAbortPreservesDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(path, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	writer, err := createAtomicWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("partial render")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Abort(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "existing" {
		t.Fatalf("abort replaced destination: %q, %v", contents, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("abort left temporary output: %v, %v", entries, err)
	}
}
