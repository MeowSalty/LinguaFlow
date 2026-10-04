package s3store

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/drivertest"
)

func testStore(t *testing.T, handler http.HandlerFunc) *Store {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store, err := New(Options{Endpoint: server.URL, Region: "test-region", Bucket: "test-bucket", Prefix: "managed", PathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider("BOUNDKEY", "bound-secret", ""), HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestRetryAfterIsPreservedWithoutProviderDiagnostics(t *testing.T) {
	store := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.Header().Set("X-Provider-Secret", "private-header")
		writeError(w, http.StatusServiceUnavailable, "SlowDown")
	})
	_, err := store.PutNew(context.Background(), "key", strings.NewReader("data"), 4)
	var retry interface{ RetryAfter() time.Duration }
	if !errors.Is(err, storage.ErrUnavailable) || !errors.As(err, &retry) || retry.RetryAfter() != 120*time.Second {
		t.Fatalf("retry delay was lost: %v", err)
	}
	if err.Error() != storage.ErrUnavailable.Error() {
		t.Fatal("provider details escaped error code")
	}
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	if delay := parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); delay != 90*time.Second {
		t.Fatalf("HTTP-date delay: %v", delay)
	}
	for _, value := range []string{"", "-1", "provider-secret", "1.5", now.Add(-time.Hour).Format(http.TimeFormat)} {
		if delay := parseRetryAfter(value, now); delay != 0 {
			t.Fatalf("invalid Retry-After accepted: %q %v", value, delay)
		}
	}
	if delay := parseRetryAfter("18446744073709551615", now); delay <= 0 {
		t.Fatal("large Retry-After overflowed")
	}
	denied := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		writeError(w, http.StatusForbidden, "AccessDenied")
	})
	_, err = denied.PutNew(context.Background(), "key", strings.NewReader("data"), 4)
	if !errors.Is(err, storage.ErrPermission) || errors.As(err, &retry) {
		t.Fatal("permission failure became retryable")
	}
}

func TestDriverContract(t *testing.T) {
	drivertest.Run(t, func(t *testing.T) storage.Driver {
		objects := make(map[string][]byte)
		var mu sync.Mutex
		return testStore(t, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if !strings.Contains(r.Header.Get("Authorization"), "Credential=BOUNDKEY/") {
				t.Error("request did not use explicit credentials")
			}
			q := r.URL.Query()
			w.Header().Set("Content-Type", "application/xml")
			if q.Has("versioning") {
				io.WriteString(w, `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"/>`)
				return
			}
			if q.Get("list-type") == "2" {
				var keys []string
				for key := range objects {
					if strings.HasPrefix(key, q.Get("prefix")) {
						keys = append(keys, key)
					}
				}
				sort.Strings(keys)
				io.WriteString(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsTruncated>false</IsTruncated>`)
				if len(keys) != 0 {
					fmt.Fprintf(w, "<Contents><Key>%s</Key><Size>%d</Size></Contents>", keys[0], len(objects[keys[0]]))
				}
				io.WriteString(w, `</ListBucketResult>`)
				return
			}
			key := strings.TrimPrefix(r.URL.Path, "/test-bucket/")
			if !strings.HasPrefix(key, "managed/") {
				t.Errorf("unmanaged request: %s", r.URL.Path)
				w.WriteHeader(400)
				return
			}
			switch r.Method {
			case http.MethodPut:
				if r.Header.Get("If-None-Match") != "*" {
					t.Error("conditional create header missing")
				}
				if _, exists := objects[key]; exists {
					writeError(w, 412, "PreconditionFailed")
					return
				}
				data, err := io.ReadAll(r.Body)
				if err != nil || int64(len(data)) != r.ContentLength {
					writeError(w, 400, "IncompleteBody")
					return
				}
				objects[key] = data
				w.Header().Set("ETag", `"multipart-not-sha-256-3"`)
			case http.MethodGet, http.MethodHead:
				data, exists := objects[key]
				if !exists {
					writeError(w, 404, "NoSuchKey")
					return
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(data)))
				w.Header().Set("ETag", `"not-a-trusted-digest"`)
				if r.Method == http.MethodGet {
					w.Write(data)
				}
			case http.MethodDelete:
				delete(objects, key)
				w.WriteHeader(204)
			default:
				t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				w.WriteHeader(500)
			}
		})
	})
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<Error><Code>%s</Code><Message>provider detail must not escape</Message></Error>", code)
}

func TestErrorsAreRedactedAndWritesAreNeverRetried(t *testing.T) {
	for _, test := range []struct {
		code   string
		status int
		want   error
	}{
		{"NoSuchKey", 404, storage.ErrNotFound}, {"NoSuchBucket", 404, storage.ErrUnavailable},
		{"AccessDenied", 403, storage.ErrPermission}, {"InvalidAccessKeyId", 403, storage.ErrAuthRequired},
		{"SignatureDoesNotMatch", 403, storage.ErrAuthRequired}, {"ExpiredToken", 400, storage.ErrAuthRequired},
		{"SlowDown", 429, storage.ErrUnavailable}, {"InternalError", 500, storage.ErrUnavailable},
		{"PreconditionFailed", 412, storage.ErrExists}, {"NotImplemented", 501, storage.ErrUnsupported},
		{"EntityTooLarge", 413, storage.ErrPayloadTooLarge}, {"QuotaExceeded", 409, storage.ErrLimit},
		{"InsufficientStorage", 507, storage.ErrLimit},
	} {
		t.Run(test.code, func(t *testing.T) {
			var count atomic.Int32
			store := testStore(t, func(w http.ResponseWriter, r *http.Request) { count.Add(1); writeError(w, test.status, test.code) })
			_, err := store.PutNew(context.Background(), "key", strings.NewReader("data"), 4)
			if !errors.Is(err, test.want) {
				t.Fatalf("want %v, got %v", test.want, err)
			}
			if count.Load() != 1 {
				t.Fatalf("write retried %d times", count.Load())
			}
			if strings.Contains(err.Error(), "provider detail") {
				t.Fatal("provider detail leaked")
			}
		})
	}
}

func TestMissingHEADRequiresAuthorizedConfirmation(t *testing.T) {
	store := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(404)
			return
		}
		writeError(w, 403, "AccessDenied")
	})
	if _, err := store.Stat(context.Background(), storage.Object{Key: "file"}); !errors.Is(err, storage.ErrPermission) {
		t.Fatalf("ambiguous 404 must not mark object missing: %v", err)
	}
}

func TestVersionsAreExactPaginatedAndIncludeMarkers(t *testing.T) {
	var calls atomic.Int32
	store := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !r.URL.Query().Has("versions") || r.URL.Query().Get("prefix") != "managed/key" {
			t.Errorf("bad version scope: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Query().Get("version-id-marker") == "" {
			io.WriteString(w, `<ListVersionsResult><IsTruncated>true</IsTruncated><NextKeyMarker>managed/key</NextKeyMarker><NextVersionIdMarker>v1</NextVersionIdMarker><Version><Key>managed/key</Key><VersionId>v1</VersionId><Size>11</Size></Version></ListVersionsResult>`)
		} else {
			io.WriteString(w, `<ListVersionsResult><IsTruncated>false</IsTruncated><DeleteMarker><Key>managed/key</Key><VersionId>deleted</VersionId></DeleteMarker><Version><Key>managed/key-neighbor</Key><VersionId>private-other</VersionId><Size>99</Size></Version></ListVersionsResult>`)
		}
	})
	versions, err := store.Versions(context.Background(), "key")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(versions) != 2 || versions[0].Version != "v1" || versions[1].Version != "deleted" || !versions[1].DeleteMarker {
		t.Fatalf("versions: %+v (%d calls)", versions, calls.Load())
	}
}

func TestVersionedDeleteRequiresVersionAndDeletesExactly(t *testing.T) {
	var deletes atomic.Int32
	store := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("versioning") {
			io.WriteString(w, `<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`)
			return
		}
		if r.Method != http.MethodDelete || r.URL.Query().Get("versionId") != "v-exact" {
			t.Errorf("unsafe delete: %s %s", r.Method, r.URL)
		}
		deletes.Add(1)
		w.WriteHeader(204)
	})
	if err := store.Delete(context.Background(), storage.Object{Key: "key"}); !errors.Is(err, storage.ErrUnsupported) {
		t.Fatalf("bare delete must not create marker: %v", err)
	}
	if deletes.Load() != 0 {
		t.Fatal("bare key was deleted")
	}
	if err := store.Delete(context.Background(), storage.Object{Key: "key", Version: "v-exact", DeleteMarker: true}); err != nil {
		t.Fatal(err)
	}
	if deletes.Load() != 1 {
		t.Fatal("exact marker not deleted")
	}
}

func TestPinnedReadRejectsProviderVersionMismatch(t *testing.T) {
	store := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("versionId") != "expected" {
			t.Error("read not pinned")
		}
		w.Header().Set("X-Amz-Version-Id", "other")
		io.WriteString(w, "bytes")
	})
	if _, err := store.Open(context.Background(), storage.Object{Key: "key", Version: "expected"}); !errors.Is(err, storage.ErrCorrupt) {
		t.Fatal(err)
	}
}

func TestCredentialsMustBeExplicit(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	if _, err := New(Options{Region: "r", Bucket: "bucket"}); !errors.Is(err, storage.ErrAuthRequired) {
		t.Fatalf("ambient credentials accepted: %v", err)
	}
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called.Store(true) }))
	defer server.Close()
	store, err := New(Options{Endpoint: server.URL, Region: "r", Bucket: "bucket", PathStyle: true, Credentials: credentials.NewStaticCredentialsProvider("", "", "")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutNew(context.Background(), "key", strings.NewReader("x"), 1); !errors.Is(err, storage.ErrAuthRequired) {
		t.Fatalf("empty provider: %v", err)
	}
	if called.Load() {
		t.Fatal("request sent with empty credentials")
	}
}

func TestVersionListingBounded(t *testing.T) {
	store := testStore(t, func(w http.ResponseWriter, r *http.Request) {
		result := struct {
			XMLName  xml.Name `xml:"ListVersionsResult"`
			Versions []struct {
				Key       string
				VersionId string
				Size      int
			} `xml:"Version"`
		}{}
		for i := 0; i < 3; i++ {
			result.Versions = append(result.Versions, struct {
				Key       string
				VersionId string
				Size      int
			}{"managed/key", strconv.Itoa(i), 1})
		}
		xml.NewEncoder(w).Encode(result)
	})
	store.maxVersions = 2
	if _, err := store.Versions(context.Background(), "key"); !errors.Is(err, storage.ErrLimit) {
		t.Fatalf("unbounded versions: %v", err)
	}
}
