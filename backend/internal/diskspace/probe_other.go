//go:build !linux && !darwin && !windows

package diskspace

import (
	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func SystemProbe(context.Context, string) (Observation, error) {
	return Observation{}, storage.ErrDiskSpaceUnknown
}
func isSpaceError(error) bool { return false }
