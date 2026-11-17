package backend

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/resourcebudget"
)

func TestAllocationDecodedTimeRetiresOnlyRFC3339Temporary(t *testing.T) {
	const stamp = "2026-11-16T01:02:03Z"
	want, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"success", "pointer", "invalid", "long-token", "cancel-before", "cancel-after-string", "pointer-cancel-after-string"} {
		t.Run(name, func(t *testing.T) {
			pointer := strings.HasPrefix(name, "pointer")
			pointee := int64(0)
			if pointer {
				pointee = int64(reflect.TypeFor[time.Time]().Size())
			}
			limit := 8 + pointee + 2*int64(len(stamp))
			b, previousOwner, previousBacking, root := allocationFailureBuild(t, limit)
			original := time.Date(2000, 1, 2, 3, 4, 5, 0, time.UTC)
			decoded := original
			var decodedPointer *time.Time
			dst := reflect.ValueOf(&decoded).Elem()
			if pointer {
				dst = reflect.ValueOf(&decodedPointer).Elem()
			}
			raw := []byte(`"` + stamp + `"`)
			if name == "invalid" {
				raw = []byte(`"2026-14-16T01:02:03Z"`)
			}
			if name == "long-token" {
				raw = []byte(`"` + strings.Repeat("x", 65) + `"`)
			}
			ctx, cancel := context.WithCancel(b.ctx)
			defer cancel()
			b.ctx = ctx
			if name == "cancel-before" {
				cancel()
			}
			if strings.HasSuffix(name, "cancel-after-string") {
				reserve := b.reserve
				copies := 0
				b.reserve = func(n int64) error {
					if err := reserve(n); err != nil {
						return err
					}
					if n == int64(len(stamp)) {
						copies++
						if copies == 2 {
							cancel()
						}
					}
					return nil
				}
			}
			nodes := []jsonNode{{start: 0, end: len(raw), child: -1, next: -1, kind: '"'}}
			err := b.decodeValue(raw, nodes, 0, dst)
			if name == "success" || name == "pointer" {
				if err != nil || pointer && (decodedPointer == nil || !decodedPointer.Equal(want)) || !pointer && !decoded.Equal(want) {
					t.Fatal("RFC3339 value changed", err, decoded, decodedPointer)
				}
				if b.Snapshot().MeasuredRequiredBytes != limit {
					t.Fatal("time temporary did not count its actual overlap", b.Snapshot(), limit)
				}
			} else {
				if name == "invalid" && (err == nil || !strings.Contains(err.Error(), "invalid bounded RFC3339 time")) {
					t.Fatal("invalid time cause changed", err)
				}
				if name == "long-token" && !errors.Is(err, ErrBuildCapacity) {
					t.Fatal("time token bound changed", err)
				}
				if strings.HasPrefix(name, "cancel") || strings.Contains(name, "-cancel-") {
					if !errors.Is(err, context.Canceled) {
						t.Fatal("time cancellation cause changed", err)
					}
				}
				if !pointer && !decoded.Equal(original) {
					t.Fatal("failed time replaced caller value", decoded)
				}
			}
			if previousBacking[0] != 42 || b.Snapshot().LiveBytes != 8+pointee || b.owned != 8 || root.Snapshot(1).Instance.PayloadBytes != 8+pointee {
				t.Fatal("RFC3339 temporary remained or a real pointee was refunded", b.Snapshot(), root.Snapshot(1))
			}
			dst.SetZero()
			decodedPointer = nil
			if err = b.retireUninstalledAllocation(pointee); err != nil {
				t.Fatal(err)
			}
			previousBacking = nil
			previousOwner.Release()
			if root.Snapshot(1).Instance != (resourcebudget.Vector{}) || b.owned != 0 {
				t.Fatal("time ownership did not drain", b.Snapshot(), root.Snapshot(1))
			}
		})
	}
}

type timeLifecycleWriter func([]byte) (int, error)

func (w timeLifecycleWriter) Write(p []byte) (int, error) { return w(p) }

func TestAllocationTimeEncodingOwnsActualScratchAndRetiresOnFailure(t *testing.T) {
	value := time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC)
	for _, name := range []string{"writer-failure", "invalid", "cancel-before", "cancel-after-scratch"} {
		t.Run(name, func(t *testing.T) {
			b, previousOwner, previousBacking, root := allocationFailureBuild(t, 8+64)
			ctx, cancel := context.WithCancel(b.ctx)
			defer cancel()
			b.ctx = ctx
			if name == "cancel-before" {
				cancel()
			}
			if name == "cancel-after-scratch" {
				reserve := b.reserve
				b.reserve = func(n int64) error {
					err := reserve(n)
					if err == nil && n == 64 {
						cancel()
					}
					return err
				}
			}
			input := value
			if name == "invalid" {
				input = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			}
			stop := errors.New("time writer stopped")
			calls := 0
			err := b.WriteJSON(timeLifecycleWriter(func(p []byte) (int, error) {
				calls++
				if calls == 2 {
					return 0, stop
				}
				return len(p), nil
			}), input)
			if name == "writer-failure" && !errors.Is(err, stop) {
				t.Fatal("writer error lost", err)
			}
			if name == "invalid" && !errors.Is(err, ErrBuildCapacity) {
				t.Fatal("RFC3339 year limit lost", err)
			}
			if strings.HasPrefix(name, "cancel") && !errors.Is(err, context.Canceled) {
				t.Fatal("encoding cancellation lost", err)
			}
			peak := int64(8)
			if name == "writer-failure" || name == "cancel-after-scratch" {
				peak += 64
			}
			if b.Snapshot().MeasuredRequiredBytes != peak || b.Snapshot().LiveBytes != 8 || b.owned != 8 || previousBacking[0] != 42 || root.Snapshot(1).Instance.PayloadBytes != 8 {
				t.Fatal("time width substituted for scratch or temporary retained", b.Snapshot(), peak, root.Snapshot(1))
			}
			previousBacking = nil
			previousOwner.Release()
			if root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
				t.Fatal(root.Snapshot(1))
			}
		})
	}
}

func TestAllocationEncodedTimeOutputSurvivesTemporaryBuild(t *testing.T) {
	b, previousOwner, previousBacking, root := allocationFailureBuild(t, 1024)
	if err := previousOwner.DetachBuild(); err != nil {
		t.Fatal(err)
	}
	value := time.Date(2026, 11, 16, 1, 2, 3, 123456789, time.UTC)
	raw, err := b.EncodeJSON(value, 4096)
	if err != nil || string(raw) != `"`+value.Format(time.RFC3339Nano)+`"` {
		t.Fatal("encoded time changed", err, string(raw))
	}
	retained := int64(cap(raw))
	if b.Snapshot().LiveBytes != retained || root.Snapshot(1).Instance.PayloadBytes != 8+retained {
		t.Fatal("encoding retained its scratch", b.Snapshot(), root.Snapshot(1))
	}
	owner, err := b.RetainedAllocation(retained)
	if err != nil {
		t.Fatal(err)
	}
	if err = owner.DetachBuild(); err != nil {
		t.Fatal(err)
	}
	b.CloseRetired()
	if string(raw) != `"`+value.Format(time.RFC3339Nano)+`"` || previousBacking[0] != 42 || root.Snapshot(1).Instance.PayloadBytes != 8+retained {
		t.Fatal("temporary close refunded live outputs", root.Snapshot(1))
	}
	raw, previousBacking = nil, nil
	owner.Release()
	previousOwner.Release()
	if root.Snapshot(1).Instance != (resourcebudget.Vector{}) {
		t.Fatal("time output did not drain", root.Snapshot(1))
	}
}
