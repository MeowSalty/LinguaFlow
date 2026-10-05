package docx

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ziputil"
)

func TestParseHonorsDeploymentArchiveLimits(t *testing.T) {
	raw := createTestDOCX(t, wrapBody(para(run("", "source"))))
	limits := ziputil.DefaultLimits()
	limits.MaxEntries = 2
	ctx := ziputil.WithLimits(context.Background(), limits)
	if _, err := New().Parse(ctx, bytes.NewReader(raw), "source.docx"); !errors.Is(err, ziputil.ErrArchiveEntriesExceeded) {
		t.Fatalf("parser ignored configured entry limit: %v", err)
	}
	limits.MaxEntries = 10
	limits.MaxExpandedBytes = 10
	ctx = ziputil.WithLimits(context.Background(), limits)
	if _, err := New().Parse(ctx, bytes.NewReader(raw), "source.docx"); !errors.Is(err, ziputil.ErrDecompressedSizeExceeded) {
		t.Fatalf("parser ignored configured expansion limit: %v", err)
	}
}
