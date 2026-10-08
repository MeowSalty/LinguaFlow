package diskspace

import (
	"bytes"
	"context"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"io"
	"math"
	"sync"
	"testing"
	"time"
)

func TestThresholdParsing(t *testing.T) {
	for _, value := range []string{"1%", "0.1%", "99.99%", "1", "1073741824"} {
		if _, err := ParseThreshold(value); err != nil {
			t.Errorf("%q: %v", value, err)
		}
	}
	for _, value := range []string{"", "0", "-1", "0%", "100%", "NaN%", "1e2", "1e-2%", " 1%", "1 %", "+1", "1GiB", "9223372036854775808"} {
		if _, err := ParseThreshold(value); err == nil {
			t.Errorf("accepted %q", value)
		}
	}
}
func TestDefaultThresholdAcrossVolumes(t *testing.T) {
	// Peak bytes are separate from the reserve: a 1% policy does not silently
	// admit a 3-copy upload whose remaining allocation crosses that reserve.
	for _, total := range []uint64{1 << 30, 16 << 30, 100 << 30, 1 << 40, 16 << 40} {
		reserve := DefaultThreshold().bytes(total)
		peak := uint64(3 * 100 << 20)
		available := reserve + peak
		c, _ := New(DefaultThreshold(), func(context.Context, string) (Observation, error) {
			return Observation{FilesystemID: "a", TotalBytes: total, AvailableBytes: available}, nil
		})
		r, err := c.Reserve(context.Background(), "objects", int64(peak))
		if err != nil {
			t.Fatal(err)
		}
		r.Release()
		available--
		if _, err = c.Reserve(context.Background(), "objects", int64(peak)); !errors.Is(err, storage.ErrDiskSpaceInsufficient) {
			t.Fatalf("total %d: %v", total, err)
		}
	}
}
func TestSharedVolumeReservationAndSettlement(t *testing.T) {
	available := uint64(1000)
	c, _ := New(Threshold{Bytes: 100}, func(context.Context, string) (Observation, error) {
		return Observation{FilesystemID: "a", TotalBytes: 1000, AvailableBytes: available}, nil
	})
	first, err := c.Reserve(context.Background(), "work", 600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Reserve(context.Background(), "objects", 301); !errors.Is(err, storage.ErrDiskSpaceInsufficient) {
		t.Fatal(err)
	}
	writer := first.Writer(writerFunc(func(p []byte) (int, error) { available -= uint64(len(p)); return len(p), nil }))
	if n, err := io.Copy(writer, bytes.NewReader(make([]byte, 400))); err != nil || n != 400 {
		t.Fatalf("copy: %d %v", n, err)
	}
	// Actual 400 + unwritten 200 + new 300 leaves exactly 100 bytes.
	second, err := c.Reserve(context.Background(), "objects", 300)
	if err != nil {
		t.Fatal(err)
	}
	first.Release()
	first.Release()
	third, err := c.Reserve(context.Background(), "cache", 200)
	if err != nil {
		t.Fatal(err)
	}
	second.Release()
	third.Release()
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
func TestExternalExhaustionAndUnknown(t *testing.T) {
	available := uint64(1000)
	unknown := false
	id := "a"
	c, _ := New(Threshold{Bytes: 100}, func(context.Context, string) (Observation, error) {
		if unknown {
			return Observation{}, errors.New("secret path")
		}
		return Observation{FilesystemID: id, TotalBytes: 1000, AvailableBytes: available}, nil
	})
	r, _ := c.Reserve(context.Background(), "work", 500)
	defer r.Release()
	available = 599
	if _, err := r.Writer(io.Discard).Write([]byte("a")); !errors.Is(err, storage.ErrDiskSpaceInsufficient) {
		t.Fatal(err)
	}
	unknown = true
	if _, err := r.Writer(io.Discard).Write([]byte("a")); !errors.Is(err, storage.ErrDiskSpaceUnknown) {
		t.Fatal(err)
	}
	unknown = false
	available = 1000
	id = "mounted-replacement"
	if _, err := r.Writer(io.Discard).Write([]byte("a")); !errors.Is(err, storage.ErrDiskSpaceUnknown) {
		t.Fatal(err)
	}
}
func TestSeparateVolumesAndConcurrentReservations(t *testing.T) {
	c, _ := New(Threshold{Bytes: 100}, func(_ context.Context, path string) (Observation, error) {
		return Observation{FilesystemID: path, TotalBytes: 1000, AvailableBytes: 1000}, nil
	})
	var wg sync.WaitGroup
	var mu sync.Mutex
	var held []*Reservation
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := c.Reserve(context.Background(), "a", 100)
			if err == nil {
				mu.Lock()
				held = append(held, r)
				mu.Unlock()
			} else if !errors.Is(err, storage.ErrDiskSpaceInsufficient) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(held) != 9 {
		t.Fatalf("reserved %d; want 9", len(held))
	}
	b, err := c.Reserve(context.Background(), "b", 900)
	if err != nil {
		t.Fatal(err)
	}
	b.Release()
	for _, r := range held {
		r.Release()
	}
}
func TestLimitsCancellationAndPartialWrite(t *testing.T) {
	c, _ := New(Threshold{Bytes: 1}, func(context.Context, string) (Observation, error) {
		return Observation{FilesystemID: "a", TotalBytes: math.MaxUint64, AvailableBytes: math.MaxUint64}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Reserve(ctx, "a", 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	r, err := c.Reserve(context.Background(), "a", math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Reserve(context.Background(), "a", math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Reserve(context.Background(), "a", 1); !errors.Is(err, storage.ErrDiskSpaceInsufficient) {
		t.Fatal(err)
	}
	r.Release()
	small, err := c.Reserve(context.Background(), "a", 10)
	if err != nil {
		t.Fatal(err)
	}
	defer small.Release()
	if _, err = small.Writer(io.Discard).Write(make([]byte, 11)); !errors.Is(err, storage.ErrPayloadTooLarge) {
		t.Fatal(err)
	}
	n, err := small.Writer(writerFunc(func([]byte) (int, error) { return 3, io.ErrUnexpectedEOF })).Write(make([]byte, 5))
	if n != 3 || !errors.Is(err, io.ErrUnexpectedEOF) || small.remaining != 7 {
		t.Fatalf("%d %v remaining=%d", n, err, small.remaining)
	}
}
func TestSystemProbe(t *testing.T) {
	before := time.Now()
	o, err := SystemProbe(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if o.FilesystemID == "" || o.TotalBytes == 0 || o.AvailableBytes > o.TotalBytes || o.ObservedAt.Before(before) {
		t.Fatalf("invalid observation: %+v", o)
	}
}

func TestSealedReservationDoesNotProbeForEmptyWrites(t *testing.T) {
	unknown := false
	c, _ := New(Threshold{Bytes: 1}, func(context.Context, string) (Observation, error) {
		if unknown {
			return Observation{}, errors.New("unknown")
		}
		return Observation{FilesystemID: "a", TotalBytes: 100, AvailableBytes: 100}, nil
	})
	r, err := c.Reserve(context.Background(), "a", 10)
	if err != nil {
		t.Fatal(err)
	}
	r.Seal()
	unknown = true
	if n, err := r.Writer(io.Discard).Write(nil); n != 0 || err != nil {
		t.Fatalf("empty write: %d %v", n, err)
	}
	if _, err := r.Writer(io.Discard).Write([]byte("x")); !errors.Is(err, storage.ErrPayloadTooLarge) {
		t.Fatal(err)
	}
	r.Release()
}
