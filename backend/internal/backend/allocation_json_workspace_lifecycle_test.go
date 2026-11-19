package backend

import (
	"context"
	"errors"
	"testing"
)

func TestAllocationJSONWorkspaceRetiresWithoutRefundingPayload(t *testing.T) {
	var rootLive int64
	ctx := WithBuildAllocation(context.Background(), 2048, func(n int64) error {
		rootLive += n
		return nil
	}, func(n int64) { rootLive -= n })
	ctx, err := WithProviderResponseAllocation(ctx, 1024)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBuildBoundsFromContext(ctx, 1024, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer b.CloseRetired()
	seed, err := b.OwnAllocation(11, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Release()
	backing := make([]byte, 11)
	backing[0] = 7
	type nested struct {
		Flag  bool
		Child struct{ Other bool }
	}
	value := nested{Flag: true}
	value.Child.Other = true
	check := func(want int64) {
		t.Helper()
		if b.Snapshot().LiveBytes != want || ProviderResponseSnapshot(ctx).LiveBytes != want || rootLive != want || backing[0] != 7 {
			t.Fatalf("payload lifetime: bounds=%d provider=%d root=%d want=%d", b.Snapshot().LiveBytes, ProviderResponseSnapshot(ctx).LiveBytes, rootLive, want)
		}
	}
	for i := 0; i < 100; i++ {
		n, err := b.MeasureJSON(value)
		if err != nil {
			t.Fatal(err)
		}
		check(11)
		encoded, err := b.EncodeJSONMeasured(value, n, 1024)
		if err != nil {
			t.Fatal(err)
		}
		check(11 + int64(len(encoded)))
		owner, err := b.RetainedAllocation(int64(cap(encoded)))
		if err != nil {
			t.Fatal(err)
		}
		var decoded nested
		if err = b.DecodeJSON(encoded, &decoded); err != nil || decoded != value {
			t.Fatal("decoded retained payload changed", err)
		}
		check(11 + int64(len(encoded)))
		decoded = nested{}
		encoded = nil
		owner.Release()
		check(11)
	}
	if _, err = b.MeasureJSON(struct{ Invalid func() }{}); err == nil {
		t.Fatal("unsupported encoder value was accepted")
	}
	check(11)
	var decoded nested
	if err = b.DecodeJSON([]byte(`{"Flag":[]}`), &decoded); err == nil {
		t.Fatal("invalid member field was accepted")
	}
	decoded = nested{}
	check(11)
	backing = nil
	seed.Release()
	b.CloseRetired()
	if rootLive != 0 || ProviderResponseSnapshot(ctx).LiveBytes != 0 {
		t.Fatal("joined JSON workspace retained live capacity")
	}
	if err = CloseProviderResponseAllocation(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = b.MeasureJSON(value); !errors.Is(err, ErrBuildCapacity) {
		t.Fatal("retired build restarted", err)
	}
}

type jsonWorkspaceNestedWriter struct {
	inside func() error
	calls  int
}

func (w *jsonWorkspaceNestedWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == 2 {
		if err := w.inside(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func TestAllocationJSONViewAndNestedWorkspaceBorrowers(t *testing.T) {
	var rootLive int64
	ctx := WithBuildAllocation(context.Background(), 4096, func(n int64) error {
		rootLive += n
		return nil
	}, func(n int64) { rootLive -= n })
	ctx, err := WithProviderResponseAllocation(ctx, 1024)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBuildBoundsFromContext(ctx, 1024, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer b.CloseRetired()
	seed, err := b.OwnAllocation(7, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Release()
	backing := make([]byte, 7)
	backing[0] = 9
	w := &jsonWorkspaceNestedWriter{inside: func() error {
		if b.fieldCache == nil {
			return errors.New("outer writer has no borrowed reflection fields")
		}
		before := b.Snapshot().LiveBytes
		if _, err := b.MeasureJSON(struct{ Flag bool }{true}); err != nil {
			return err
		}
		if b.jsonOperations != 1 || b.fieldCache == nil || b.Snapshot().LiveBytes <= before {
			return errors.New("inner JSON operation refunded the outer writer workspace")
		}
		return nil
	}}
	if err = b.WriteJSON(w, struct{ Word string }{"kept"}); err != nil || w.calls == 0 {
		t.Fatal("nested writer", err)
	}
	if b.Snapshot().LiveBytes != 7 || rootLive != 7 || ProviderResponseSnapshot(ctx).LiveBytes != 7 {
		t.Fatal("joined nested writer retained workspace")
	}
	viewBounds, err := NewBuildBoundsFromContext(ctx, 1024, 1024)
	if err != nil {
		t.Fatal(err)
	}
	defer viewBounds.CloseRetired()
	raw := []byte(`{"Word":"kept"}`)
	view, err := NewJSONView(viewBounds, raw)
	if err != nil {
		t.Fatal(err)
	}
	viewBytes := viewBounds.Snapshot().LiveBytes
	for i := 0; i < 100; i++ {
		var decoded struct{ Word string }
		if err = view.DecodeIntoBounded(b, &decoded, 1024); err != nil || decoded.Word != "kept" {
			t.Fatal("subtree decode", err)
		}
		if b.Snapshot().LiveBytes != 11 || viewBounds.Snapshot().LiveBytes != viewBytes || rootLive != 11+viewBytes || ProviderResponseSnapshot(ctx).LiveBytes != 11+viewBytes || backing[0] != 9 {
			t.Fatal("subtree decode retained workspace after its last borrower")
		}
		if _, err = b.MeasureJSON(decoded); err != nil {
			t.Fatal(err)
		}
		if b.Snapshot().LiveBytes != 11 || viewBounds.Snapshot().LiveBytes != viewBytes || rootLive != 11+viewBytes || ProviderResponseSnapshot(ctx).LiveBytes != 11+viewBytes || backing[0] != 9 {
			t.Fatal("subtree decode changed the shared view or refunded its selected string")
		}
		output, err := b.RetainedAllocation(4)
		if err != nil {
			t.Fatal(err)
		}
		decoded.Word = ""
		output.Release()
		if b.Snapshot().LiveBytes != 7 || rootLive != 7+viewBytes {
			t.Fatal("subtree workspace accumulated across members")
		}
	}
	view = JSONValue{}
	raw = nil
	viewBounds.CloseRetired()
	backing = nil
	seed.Release()
	b.CloseRetired()
	if rootLive != 0 || ProviderResponseSnapshot(ctx).LiveBytes != 0 {
		t.Fatal("joined view or original meter retained capacity")
	}
	if err = CloseProviderResponseAllocation(ctx); err != nil {
		t.Fatal(err)
	}
}
