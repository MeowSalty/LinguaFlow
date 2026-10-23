//go:build !windows

package storagemigrate

import "os"

func syncManifestDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
