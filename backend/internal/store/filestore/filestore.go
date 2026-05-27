package filestore

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type FileRef struct {
	RelativePath string
	AbsolutePath string
	Filename     string
}

type LocalStore struct {
	root string
}

func NewLocal(root string) (*LocalStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("filestore: empty root")
	}
	cleaned := filepath.Clean(root)
	if err := os.MkdirAll(cleaned, 0o755); err != nil {
		return nil, fmt.Errorf("filestore: mkdir root: %w", err)
	}
	return &LocalStore{root: cleaned}, nil
}

func (s *LocalStore) Root() string {
	return s.root
}

func (s *LocalStore) SaveUpload(ctx context.Context, jobID, subJobID int, filename string, r io.Reader) (FileRef, error) {
	ref, err := s.PrepareUpload(jobID, subJobID, filename)
	if err != nil {
		return FileRef{}, err
	}
	if err := s.Write(ctx, ref.RelativePath, r); err != nil {
		return FileRef{}, err
	}
	return ref, nil
}

func (s *LocalStore) PrepareUpload(jobID, subJobID int, filename string) (FileRef, error) {
	return s.buildRef("uploads", jobID, subJobID, filename)
}

func (s *LocalStore) PrepareOutput(jobID, subJobID int, filename string) (FileRef, error) {
	return s.buildRef("outputs", jobID, subJobID, filename)
}

func (s *LocalStore) Write(ctx context.Context, relativePath string, r io.Reader) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	absPath, err := s.resolveRelativePath(relativePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return fmt.Errorf("filestore: mkdir parent: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(absPath), ".upload-*")
	if err != nil {
		return fmt.Errorf("filestore: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}
	if _, err := io.Copy(tmp, r); err != nil {
		cleanup()
		return fmt.Errorf("filestore: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("filestore: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, absPath); err != nil {
		cleanup()
		return fmt.Errorf("filestore: rename temp: %w", err)
	}
	return nil
}

func (s *LocalStore) Open(relativePath string) (*os.File, error) {
	absPath, err := s.resolveRelativePath(relativePath)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("filestore: open %s: %w", relativePath, err)
	}
	return file, nil
}

func (s *LocalStore) Absolute(relativePath string) (string, error) {
	return s.resolveRelativePath(relativePath)
}

func (s *LocalStore) Delete(relativePath string) error {
	absPath, err := s.resolveRelativePath(relativePath)
	if err != nil {
		return err
	}
	if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("filestore: delete %s: %w", relativePath, err)
	}
	return nil
}

func (s *LocalStore) DeleteJob(jobID int) error {
	jobRoot := filepath.Join(s.root, fmt.Sprintf("job-%d", jobID))
	if err := os.RemoveAll(jobRoot); err != nil {
		return fmt.Errorf("filestore: delete job root: %w", err)
	}
	for _, bucket := range []string{"uploads", "outputs"} {
		bucketJobRoot := filepath.Join(s.root, bucket, fmt.Sprintf("job-%d", jobID))
		if err := os.RemoveAll(bucketJobRoot); err != nil {
			return fmt.Errorf("filestore: delete job bucket root: %w", err)
		}
	}
	return nil
}

func (s *LocalStore) buildRef(bucket string, jobID, subJobID int, filename string) (FileRef, error) {
	if jobID <= 0 || subJobID <= 0 {
		return FileRef{}, fmt.Errorf("filestore: invalid job/subjob id")
	}
	cleanName := sanitizeFilename(filename)
	rel := path.Join(bucket, fmt.Sprintf("job-%d", jobID), fmt.Sprintf("subjob-%d", subJobID), cleanName)
	absPath, err := s.resolveRelativePath(rel)
	if err != nil {
		return FileRef{}, err
	}
	return FileRef{RelativePath: rel, AbsolutePath: absPath, Filename: cleanName}, nil
}

func (s *LocalStore) resolveRelativePath(relativePath string) (string, error) {
	cleanRel := path.Clean(strings.TrimSpace(strings.ReplaceAll(relativePath, "\\", "/")))
	if cleanRel == "." || cleanRel == "" || strings.HasPrefix(cleanRel, "../") || cleanRel == ".." || path.IsAbs(cleanRel) {
		return "", fmt.Errorf("filestore: invalid relative path %q", relativePath)
	}
	return filepath.Join(s.root, filepath.FromSlash(cleanRel)), nil
}

func sanitizeFilename(name string) string {
	base := strings.TrimSpace(filepath.Base(name))
	if base == "" || base == "." || base == ".." {
		base = "file"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	base = replacer.Replace(base)
	if strings.TrimSpace(base) == "" {
		return "file"
	}
	return base
}
