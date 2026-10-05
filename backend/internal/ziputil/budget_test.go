package ziputil

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"testing"
)

func TestArchiveAggregateExpansionAndRepeatedReads(t *testing.T) {
	raw := makeTestZip(t, map[string][]byte{"a": []byte("123456"), "b": []byte("abcdef")})
	limits := Limits{MaxEntries: 2, MaxExpandedBytes: 10, MaxOutputBytes: 1000}
	ctx := WithLimits(context.Background(), limits)
	if _, err := OpenZipContext(ctx, bytes.NewReader(raw), 1000); !errors.Is(err, ErrDecompressedSizeExceeded) {
		t.Fatalf("aggregate expansion accepted: %v", err)
	}
	limits.MaxExpandedBytes = 12
	zr, err := OpenZipContext(WithLimits(context.Background(), limits), bytes.NewReader(raw), 1000)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := ReadEntry(zr, "a", 10); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := ReadEntry(zr, "b", 10); !errors.Is(err, ErrDecompressedSizeExceeded) {
			t.Fatalf("repeated-read budget was reset: %v", err)
		}
	}
}

func TestPreflightCountsActualCentralDirectory(t *testing.T) {
	raw := makeTestZip(t, map[string][]byte{"a": nil, "b": nil, "c": nil})
	// 伪造的 EOCD 计数无法在 archive/zip 分配 File 切片之前躲过限制。
	// 预检统计的是真实的中央目录头数量。
	end := len(raw) - 22
	binary.LittleEndian.PutUint16(raw[end+8:end+10], 1)
	binary.LittleEndian.PutUint16(raw[end+10:end+12], 1)
	limits := Limits{MaxEntries: 2, MaxExpandedBytes: 100, MaxOutputBytes: 1000}
	if _, err := OpenZipContext(WithLimits(context.Background(), limits), bytes.NewReader(raw), 1000); !errors.Is(err, ErrArchiveEntriesExceeded) {
		t.Fatalf("spoofed directory count accepted: %v", err)
	}
}

func TestArchiveReadAndCopyHonorCancellation(t *testing.T) {
	raw := makeTestZip(t, map[string][]byte{"a": bytes.Repeat([]byte("x"), 10000)})
	ctx, cancel := context.WithCancel(context.Background())
	zr, err := OpenZipContext(ctx, bytes.NewReader(raw), 1000)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.Read(make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := io.ReadAll(reader); !errors.Is(err, context.Canceled) {
		t.Fatalf("decompression ignored cancellation: %v", err)
	}
	zw := zip.NewWriter(io.Discard)
	if err := CopyEntryUnbounded(zw, zr.File[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("asset copy ignored cancellation: %v", err)
	}
}

func TestFinalArchiveOutputIncludesDirectoryBudget(t *testing.T) {
	ctx := WithLimits(context.Background(), Limits{MaxEntries: 10, MaxExpandedBytes: 100, MaxOutputBytes: 40})
	var out bytes.Buffer
	zw := zip.NewWriter(OutputWriter(ctx, &out))
	if _, err := zw.Create("empty"); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); !errors.Is(err, ErrOutputSizeExceeded) {
		t.Fatalf("central directory exceeded output budget: %v", err)
	}
	if out.Len() > 40 {
		t.Fatal("writer published bytes beyond its output budget")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OutputWriter(canceled, io.Discard).Write([]byte("a")); !errors.Is(err, context.Canceled) {
		t.Fatal("output ignored cancellation")
	}
}

func TestInvalidReadLimitsCannotOverflow(t *testing.T) {
	for _, max := range []int64{-1, math.MaxInt64} {
		if _, err := ReadBounded(bytes.NewReader([]byte("x")), max); !errors.Is(err, ErrDecompressedSizeExceeded) {
			t.Fatalf("invalid limit %d accepted: %v", max, err)
		}
	}
}
