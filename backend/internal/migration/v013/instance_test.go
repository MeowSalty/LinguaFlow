package v013

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestLegacyConvertRejectsBeforeUsingClient(t *testing.T) {
	// A nil client would panic if conversion attempted to query or mutate it.
	if _, err := Convert(context.Background(), nil, nil, "server"); !errors.Is(err, ErrTargetIncompatible) {
		t.Fatalf("unsupported conversion: %v", err)
	}
	client := testClient(t)
	writes := 0
	client.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, mutation ent.Mutation) (ent.Value, error) {
			writes++
			return next.Mutate(ctx, mutation)
		})
	})
	if _, err := Convert(t.Context(), client, credentialTestKeyring(t, "key", "key"), "local"); !errors.Is(err, ErrTargetIncompatible) {
		t.Fatal(err)
	}
	if writes != 0 || client.InstanceInitialization.Query().ExistX(t.Context()) {
		t.Fatal("unsupported conversion modified the target")
	}
}

func TestLegacyPostgresRejectsBeforeConnectionOrPublication(t *testing.T) {
	for _, apply := range []bool{false, true} {
		called := false
		_, err := RunPostgres(t.Context(), nil, nil, apply, func() error {
			called = true
			return nil
		})
		if !errors.Is(err, ErrTargetIncompatible) || called {
			t.Fatalf("unsupported PostgreSQL migration reached database/publication: %v", err)
		}
	}
}

func legacyMigrationString(value string) *string { return &value }
