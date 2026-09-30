package backend

import (
	"context"
	"fmt"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

func TestCredentialPolicyFailuresDoNotRetry(t *testing.T) {
	for _, cause := range []error{credential.ErrRevoked, credential.ErrBackendDeleted, credential.ErrUnavailable, credential.ErrEndpoint} {
		err := fmt.Errorf("provider SDK: %w", cause)
		if IsRetryable(err) {
			t.Fatalf("policy failure considered transient: %v", err)
		}
		calls := 0
		if err := WithRetry(context.Background(), RetryPolicy{MaxAttempts: 3}, func() error { calls++; return err }); err == nil {
			t.Fatal("failure disappeared")
		}
		if calls != 1 {
			t.Fatalf("policy failure retried %d times", calls)
		}
	}
}
