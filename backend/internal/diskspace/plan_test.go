package diskspace

import (
	"context"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"math"
	"testing"
)

func TestCheckPlanAggregatesFilesystemAliasesAndReservations(t *testing.T) {
	c, _ := New(Threshold{Bytes: 100}, func(_ context.Context, path string) (Observation, error) {
		id := "a"
		if path == "separate" {
			id = "b"
		}
		return Observation{FilesystemID: id, TotalBytes: 1000, AvailableBytes: 1000}, nil
	})
	ctx := context.Background()
	if err := c.CheckPlan(ctx, map[string]int64{"work": 500, "objects": 500}); !errors.Is(err, storage.ErrDiskSpaceInsufficient) {
		t.Fatal(err)
	}
	if err := c.CheckPlan(ctx, map[string]int64{"work": 500, "separate": 500}); err != nil {
		t.Fatal(err)
	}
	r, err := c.Reserve(ctx, "spool", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Release()
	if err = c.CheckPlan(ctx, map[string]int64{"work": 400, "objects": 400}); err != nil {
		t.Fatal(err)
	}
	if err = c.CheckPlan(ctx, map[string]int64{"work": 400, "objects": 401}); !errors.Is(err, storage.ErrDiskSpaceInsufficient) {
		t.Fatal(err)
	}
	if err = c.Check(ctx, "work", 800); err != nil {
		t.Fatalf("plan retained capacity: %v", err)
	}
}

func TestCheckPlanUnknownAndOverflow(t *testing.T) {
	c, _ := New(Threshold{Bytes: 1}, func(_ context.Context, path string) (Observation, error) {
		if path == "unknown" {
			return Observation{}, errors.New("unknown")
		}
		return Observation{FilesystemID: "a", TotalBytes: math.MaxUint64, AvailableBytes: math.MaxUint64}, nil
	})
	ctx := context.Background()
	if err := c.CheckPlan(ctx, map[string]int64{"a": -1}); !errors.Is(err, storage.ErrPayloadTooLarge) {
		t.Fatal(err)
	}
	if err := c.CheckPlan(ctx, map[string]int64{"a": 1, "unknown": 0}); !errors.Is(err, storage.ErrDiskSpaceUnknown) {
		t.Fatal(err)
	}
	if err := c.CheckPlan(ctx, map[string]int64{"a": math.MaxInt64, "b": math.MaxInt64, "c": 2}); !errors.Is(err, storage.ErrPayloadTooLarge) {
		t.Fatal(err)
	}
}
