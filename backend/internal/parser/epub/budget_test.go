package epub

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ziputil"
)

func TestOptionalMetadataCannotHideExhaustedArchiveBudget(t *testing.T) {
	raw := createTestEPUB(t, []testChapter{{filename: "OEBPS/chapter.xhtml", content: "<p>Text</p>", id: "chapter"}})
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, file := range zr.File {
		total += int64(file.UncompressedSize64)
	}
	// 首次打开在声明的总量之内，但为提取可选的标题/导航信息而反复读取
	// OPF 会消耗真实的工作量，最终超出预算。
	limits := ziputil.DefaultLimits()
	limits.MaxExpandedBytes = total
	ctx := ziputil.WithLimits(context.Background(), limits)
	if _, err := New().Parse(ctx, bytes.NewReader(raw), "book.epub"); !errors.Is(err, ziputil.ErrDecompressedSizeExceeded) {
		t.Fatalf("optional metadata hid budget exhaustion: %v", err)
	}
}
