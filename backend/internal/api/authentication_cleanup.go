package api

import (
	"context"
	"time"
)

// Expiry checks in the service remain authoritative. This maintenance loop
// bounds retention of expired provider profile data and one-time proofs.
func (s *Server) runAuthenticationCleanup(ctx context.Context) {
	if s.externalAuth == nil || s.isLocal() {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := s.externalAuth.CleanupExpired(cleanupCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			// Do not log SQL errors: they may contain authentication row data.
			s.logger.Warn("expired authentication state cleanup failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
