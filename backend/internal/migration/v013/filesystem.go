package v013

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

const localDatabaseName = "linguaflow.db"
const localReceiptName = ".linguaflow-v013-receipt.json"

func localPaths(dataDir string) (string, string, error) {
	if strings.TrimSpace(dataDir) == "" {
		return "", "", errors.New("local migration requires an explicit data directory")
	}
	path, err := filepath.Abs(dataDir)
	if err != nil {
		return "", "", err
	}
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	if path == parent {
		return "", "", errors.New("a filesystem root cannot be a local data directory")
	}
	if err := validateLocalVolume(path); err != nil {
		return "", "", err
	}
	// The source can be absent during interrupted publication. Its ancestors
	// must still be real local directories before reading a recovery journal.
	if err := validatePath(parent, true); err != nil {
		return "", "", err
	}
	return path, parent, nil
}

func validatePath(path string, directory bool) error {
	for current := filepath.Clean(path); ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if err := validateOrdinary(current, info); err != nil {
			return err
		}
		if current == path && directory != info.IsDir() {
			return fmt.Errorf("unexpected file type: %s", path)
		}
		if current != path && !info.IsDir() {
			return fmt.Errorf("path ancestor is not a directory: %s", current)
		}
		if filepath.Dir(current) == current {
			return nil
		}
	}
}

func validateOrdinary(path string, info fs.FileInfo) error {
	if !info.IsDir() && !info.Mode().IsRegular() {
		return fmt.Errorf("migration refuses links and special files: %s", path)
	}
	return rejectReparsePoint(path)
}

func newRunID() (string, error) {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func copyLocalFiles(ctx context.Context, source, target string) error {
	return copySQLiteFiles(ctx, source, target, true, "")
}

func copySQLiteFiles(ctx context.Context, source, target string, local bool, selectedKeyring string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err := validateOrdinary(path, info); err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		if filepath.Dir(relative) == "." {
			switch strings.ToLower(relative) {
			case localDatabaseName, localDatabaseName + "-wal", localDatabaseName + "-shm", localDatabaseName + "-journal":
				if info.IsDir() {
					return fmt.Errorf("database path is a directory: %s", relative)
				}
				return nil
			case localReceiptName:
				return errors.New("local directory already contains a migration receipt")
			}
		}
		destination := filepath.Join(target, relative)
		if info.IsDir() {
			if err := validateLocalVolume(path); err != nil {
				return err
			}
			return credential.PreparePrivateDirectory(destination)
		}
		// Re-publish existing deployment keys through the same ACL policy as
		// startup; a byte copy into an inherited Windows ACL is not sufficient.
		selected := selectedKeyring != "" && sameLocalPath(path, selectedKeyring)
		if relative == "credentials-keyring.json" || relative == "instance-secret" || relative == "jwt-secret" || selected {
			if (local && relative == "credentials-keyring.json") || selected {
				if _, err := credential.LoadKeyring(path); err != nil {
					return err
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			published, err := credential.PublishPrivateFile(destination, data)
			if err == nil && !published {
				err = errors.New("staging key unexpectedly exists")
			}
			return err
		}
		return copyRegularFile(ctx, path, destination, info)
	})
}

func copyRegularFile(ctx context.Context, source, target string, expected fs.FileInfo) (err error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, input.Close()) }()
	actual, err := input.Stat()
	if err != nil {
		return err
	}
	if !actual.Mode().IsRegular() || !os.SameFile(expected, actual) {
		return fmt.Errorf("source file changed during copy: %s", source)
	}
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, output.Close()) }()
	if _, err := io.Copy(output, &contextReader{ctx: ctx, reader: input}); err != nil {
		return err
	}
	after, err := input.Stat()
	if err != nil {
		return err
	}
	if actual.Size() != after.Size() || !actual.ModTime().Equal(after.ModTime()) {
		return fmt.Errorf("source file changed during copy; stop the old service: %s", source)
	}
	return output.Sync()
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func removeStaging(path, parent, identity string) error {
	if !sameLocalPath(filepath.Dir(path), parent) || !strings.Contains(filepath.Base(path), ".v013-stage-") {
		return errors.New("refusing to remove an unrecognized staging path")
	}
	if err := validatePath(path, true); err != nil {
		return err
	}
	actual, err := directoryIdentity(path)
	if err != nil {
		return err
	}
	if actual != identity {
		return errors.New("staging directory identity changed; preserved for inspection")
	}
	return os.RemoveAll(path)
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func renameAbsent(source, target string) error {
	exists, err := pathExists(target)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("refusing to replace existing directory: %s", target)
	}
	return renameLocalNoReplace(source, target)
}
