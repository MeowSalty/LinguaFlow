// Command ent-generate publishes only fully generated and formatted ent files.
// Windows readers may memory-map an existing file, preventing truncation while
// allowing replacement. Generate off-tree, then replace each output atomically.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"entgo.io/ent/entc"
	"entgo.io/ent/entc/gen"
)

func main() {
	if err := generate(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func generate() error {
	target, err := os.Getwd()
	if err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", "linguaflow-ent-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	if err := entc.Generate("./schema", &gen.Config{Target: temporary, Package: "github.com/MeowSalty/LinguaFlow/backend/internal/ent", Features: []gen.Feature{gen.FeatureExecQuery}}); err != nil {
		return err
	}
	return filepath.WalkDir(temporary, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(temporary, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, relative)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Preserve timestamps and avoid touching unrelated unchanged generated files.
		if existing, err := os.ReadFile(destination); err == nil && string(existing) == string(data) {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0755); err != nil {
			return err
		}
		staged, err := os.CreateTemp(filepath.Dir(destination), ".ent-generated-*")
		if err != nil {
			return err
		}
		name := staged.Name()
		defer os.Remove(name)
		if _, err = staged.Write(data); err != nil {
			staged.Close()
			return err
		}
		if err = staged.Close(); err != nil {
			return err
		}
		if err = os.Chmod(name, 0644); err != nil {
			return err
		}
		for attempt := 0; attempt < 10; attempt++ {
			if err = os.Rename(name, destination); err == nil {
				return nil
			}
			time.Sleep(50 * time.Millisecond)
		}
		return fmt.Errorf("publish generated file %s: %w", relative, err)
	})
}
