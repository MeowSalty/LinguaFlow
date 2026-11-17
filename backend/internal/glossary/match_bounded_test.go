package glossary

import (
	"context"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"strings"
	"testing"
)

func TestSnapshotNoHitComparisonBorrowsWithoutAllocation(t *testing.T) {
	b, _ := backend.NewBuildBounds(64, 0, 1)
	head := &EntrySnapshot{Entry: Entry{Source: strings.Repeat("a", 64) + "b"}}
	_, err := LookupEntrySnapshotContextBounded(context.Background(), b, head, strings.Repeat("a", 4096), false)
	if err != nil || b.Snapshot().AllocationBytes != 0 {
		t.Fatalf("borrowed unmatched scan allocated: snapshot%+v err%v", b.Snapshot(), err)
	}
}
func TestSnapshotMatchHonorsContextAndByteSensitiveText(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b, _ := backend.NewBuildBounds(1024, 0)
	_, err := LookupEntrySnapshotContextBounded(ctx, b, &EntrySnapshot{Entry: Entry{Source: "a"}}, "a", false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v", err)
	}
	b, _ = backend.NewBuildBounds(1024, 0)
	ok, err := sourceContainsBounded(context.Background(), b, "\xff", "\xfe", false)
	if err != nil || ok {
		t.Fatalf("byte-sensitive match changed: %v %v", ok, err)
	}
}
