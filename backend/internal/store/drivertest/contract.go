// Package drivertest 为受支持的适配器提供共享的语义测试。
package drivertest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func Run(t *testing.T, create func(*testing.T) storage.Driver) {
	t.Helper()
	t.Run("immutable roundtrip and exact delete", func(t *testing.T) {
		driver := create(t)
		ctx := context.Background()
		payload := []byte("complete original bytes\x00\xff\n")
		object, err := driver.PutNew(ctx, "sources/revision-1/random", bytes.NewReader(payload), int64(len(payload)))
		if err != nil {
			t.Fatal(err)
		}
		if object.Key != "sources/revision-1/random" || object.Size != int64(len(payload)) {
			t.Fatalf("unexpected object: %+v", object)
		}
		if _, err := driver.PutNew(ctx, object.Key, strings.NewReader("replace"), 7); !errors.Is(err, storage.ErrExists) {
			t.Fatalf("overwrite: %v", err)
		}
		reader, err := driver.Open(ctx, object)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := io.ReadAll(reader)
		reader.Close()
		if err != nil || !bytes.Equal(payload, actual) {
			t.Fatalf("read: %q, %v", actual, err)
		}
		stat, err := driver.Stat(ctx, object)
		if err != nil || stat.Size != int64(len(payload)) || stat.SHA256 != "" {
			t.Fatalf("stat must return observation without trusted digest: %+v %v", stat, err)
		}
		other, err := driver.PutNew(ctx, object.Key+"-other", strings.NewReader("other"), 5)
		if err != nil {
			t.Fatal(err)
		}
		if err := driver.Delete(ctx, object); err != nil {
			t.Fatal(err)
		}
		if err := driver.Delete(ctx, object); err != nil {
			t.Fatalf("repeat delete: %v", err)
		}
		if _, err := driver.Stat(ctx, object); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("deleted: %v", err)
		}
		if _, err := driver.Stat(ctx, other); err != nil {
			t.Fatalf("delete affected another key: %v", err)
		}
	})
	t.Run("empty object", func(t *testing.T) {
		driver := create(t)
		object, err := driver.PutNew(context.Background(), "empty", strings.NewReader(""), 0)
		if err != nil {
			t.Fatal(err)
		}
		reader, err := driver.Open(context.Background(), object)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		data, err := io.ReadAll(reader)
		if err != nil || len(data) != 0 {
			t.Fatalf("empty read: %q %v", data, err)
		}
	})
	t.Run("strict sizes", func(t *testing.T) {
		for _, test := range []struct {
			name, value string
			size        int64
		}{
			{"short", "small", 7}, {"long", "excess", 3}, {"zero", "unexpected", 0}, {"negative", "", -1},
		} {
			t.Run(test.name, func(t *testing.T) {
				driver := create(t)
				if _, err := driver.PutNew(context.Background(), "bad-size", strings.NewReader(test.value), test.size); err == nil {
					t.Fatal("wrong size accepted")
				}
				// 失败的远程请求可能已经写入了字节。精确清理必须安全；
				// 在确认之前，服务层继续保留计量。
				if err := driver.Delete(context.Background(), storage.Object{Key: "bad-size"}); err != nil {
					t.Fatal(err)
				}
			})
		}
	})
	t.Run("invalid keys", func(t *testing.T) {
		driver := create(t)
		for _, key := range []string{"", "/absolute", "../escape", "a/../escape", "a//b", "a\\b", "a:stream", "a/./b", ".lf-write-reserved", "CON", "a/NUL.txt", "a/ trailing", "trailing.", "a\x00b"} {
			if _, err := driver.PutNew(context.Background(), key, strings.NewReader("x"), 1); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("%q: %v", key, err)
			}
			if err := driver.Delete(context.Background(), storage.Object{Key: key}); !errors.Is(err, storage.ErrInvalidKey) {
				t.Errorf("delete %q: %v", key, err)
			}
		}
	})
	t.Run("cancel before IO", func(t *testing.T) {
		driver := create(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := driver.PutNew(ctx, "cancelled", strings.NewReader("x"), 1); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel put: %v", err)
		}
		if _, err := driver.Open(ctx, storage.Object{Key: "cancelled"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel open: %v", err)
		}
		if _, err := driver.Stat(ctx, storage.Object{Key: "cancelled"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel stat: %v", err)
		}
		if err := driver.Delete(ctx, storage.Object{Key: "cancelled"}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel delete: %v", err)
		}
	})
}
