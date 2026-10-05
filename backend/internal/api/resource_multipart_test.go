package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func TestStorageMultipartBoundsCleanupAndAdmission(t *testing.T) {
	s, client, _, _, _ := storageAPIServer(t)
	cfg := config.DefaultStorageConfig()
	cfg.Limits.MaxFileBytes = 8
	cfg.Limits.MaxTempBytes = 1 << 20
	cfg.Limits.MaxConcurrency = 1
	space := client.StorageSpace.Query().OnlyX(context.Background())
	s.storageSvc.Configure(cfg, "sqlite", space.ID)
	makeRequest := func(contents ...string) (*httptest.ResponseRecorder, *bytes.Buffer, string) {
		t.Helper()
		body := new(bytes.Buffer)
		w := multipart.NewWriter(body)
		for _, text := range contents {
			p, err := w.CreateFormFile("files", "source.txt")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.WriteString(p, text); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.WriteField("paths", "sub/source.txt"); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		return httptest.NewRecorder(), body, w.FormDataContentType()
	}
	recorder, body, contentType := makeRequest("good", "too large")
	r := httptest.NewRequest("POST", "/upload", body)
	r.Header.Set("Content-Type", contentType)
	r.ContentLength = -1 // 分块传输（chunked）输入同样必须受限。
	if _, _, err := s.parseResourceMultipart(recorder, r, 2); !errors.Is(err, service.ErrStorageTooLarge) {
		t.Fatalf("oversize=%v", err)
	}
	// 被拒绝请求产生的所有临时残留文件都必须已被清理。
	err := filepath.WalkDir(s.serverCfg.DataDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && len(entry.Name()) >= 5 && entry.Name()[:5] == "read-" {
			t.Errorf("temporary file leaked: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder, body, contentType = makeRequest("valid")
	r = httptest.NewRequest("POST", "/upload", body)
	r.Header.Set("Content-Type", contentType)
	form, release, err := s.parseResourceMultipart(recorder, r, 1)
	if err != nil {
		t.Fatal(err)
	}
	f, err := form.File["files"][0].Open()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil || string(got) != "valid" || form.Value["paths"][0] != "sub/source.txt" {
		t.Fatalf("form=%+v bytes=%q err=%v", form, got, err)
	}
	if _, _, _, err := s.resourceSvc.BeginUpload(context.Background()); !errors.Is(err, storage.ErrPayloadTooLarge) {
		t.Fatalf("admission=%v", err)
	}
	release()
	_, _, nextRelease, err := s.resourceSvc.BeginUpload(context.Background())
	if err != nil {
		t.Fatalf("admission not released: %v", err)
	}
	nextRelease()
}
