package operation

import (
	"errors"
	"os"
	"path/filepath"
)

// ProtectionBinding survives journal loss and is durable before preparing.
// Its immutable digest is linked to the same baseline as database receipts.
type ProtectionBinding struct {
	Version        int    `json:"version"`
	ID             string `json:"operation_id"`
	PlanDigest     string `json:"plan_digest"`
	BaselineDigest string `json:"baseline_digest"`
	Directory      string `json:"directory"`
}

func BindProtection(directory string, p *Plan, j *Journal) error {
	b := &ProtectionBinding{Version: Version, ID: p.ID, PlanDigest: Digest(p), BaselineDigest: BaselineDigest(p), Directory: j.BackupDirectory}
	if b.BaselineDigest == "" || !filepath.IsAbs(b.Directory) || filepath.Clean(filepath.Dir(b.Directory)) != filepath.Clean(directory) {
		return Fail(BackupIncomplete, "protection must belong to this private operation")
	}
	path := filepath.Join(directory, "protection-binding.json")
	if _, err := os.Lstat(path); err == nil {
		var existing ProtectionBinding
		if err := Read(path, &existing); err != nil || existing != *b {
			return Fail(Incompatible, "immutable protection binding changed")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return Write(path, b, true)
}

func LoadProtectionBinding(directory string, p *Plan, j *Journal) (bool, error) {
	var b ProtectionBinding
	path := filepath.Join(directory, "protection-binding.json")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if err := Read(path, &b); err != nil {
		return false, err
	}
	if b.Version != Version || b.ID != p.ID || b.PlanDigest != Digest(p) || b.BaselineDigest != BaselineDigest(p) || b.BaselineDigest == "" || !filepath.IsAbs(b.Directory) || filepath.Clean(filepath.Dir(b.Directory)) != filepath.Clean(directory) || (j.BackupDirectory != "" && b.Directory != j.BackupDirectory) {
		return false, Fail(Incompatible, "protection binding differs from the original baseline")
	}
	j.BackupDirectory, j.BaselineDigest = b.Directory, b.BaselineDigest
	return true, nil
}
