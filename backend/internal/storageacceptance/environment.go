// Package storageacceptance contains opt-in fixtures for real deployment tests.
// It never changes existing buckets or database schemas.
package storageacceptance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/s3store"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/storeutil"
	"github.com/aws/aws-sdk-go-v2/aws"
)

func require(t testing.TB, name string) string {
	t.Helper()
	if os.Getenv("LINGUAFLOW_STORAGE_ACCEPTANCE") != "1" {
		t.Skip("real deployment not verified: set LINGUAFLOW_STORAGE_ACCEPTANCE=1 and isolated test environment")
	}
	value := os.Getenv(name)
	if value == "" {
		t.Skip("real deployment not verified: missing " + name)
	}
	return value
}
func suffix(t testing.TB) string {
	t.Helper()
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal("test namespace generation failed")
	}
	return hex.EncodeToString(b[:])
}

// Postgres creates a fresh, random schema and removes only that schema afterwards.
// The DSN must name a dedicated acceptance database and use PostgreSQL URL syntax.
func Postgres(t *testing.T) *ent.Client {
	t.Helper()
	dsn := require(t, "LINGUAFLOW_TEST_POSTGRES_DSN")
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		t.Fatal("acceptance DSN must be a PostgreSQL URL")
	}
	cfg := config.DefaultServerConfig()
	cfg.Database = config.DatabaseConfig{Driver: config.DatabaseDriverPostgres, DSN: dsn, MaxOpenConns: 8, MaxIdleConns: 4}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db, root, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal("cannot connect to acceptance PostgreSQL (connection details redacted)")
	}
	schema := "lf_storage_test_" + suffix(t)
	if _, err = db.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		_ = root.Close()
		t.Fatal("cannot create isolated acceptance schema")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := db.ExecContext(cleanup, `DROP SCHEMA "`+schema+`" CASCADE`); err != nil {
			t.Error("cannot remove the acceptance schema")
		}
		_ = root.Close()
	})
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	cfg.Database.DSN = parsed.String()
	_, client, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal("cannot connect to isolated acceptance schema")
	}
	t.Cleanup(func() { _ = client.Close() })
	if err = client.Schema.Create(ctx); err != nil {
		t.Fatal("cannot initialize isolated acceptance schema")
	}
	return client
}

type S3 struct {
	*s3store.Store
	Options s3store.Options
	mu      sync.Mutex
	keys    map[string]bool
}

// NewS3 assigns a random prefix under an explicitly configured test prefix.
func NewS3(t *testing.T) *S3 {
	t.Helper()
	bucket := require(t, "LINGUAFLOW_TEST_S3_BUCKET")
	prefix := require(t, "LINGUAFLOW_TEST_S3_PREFIX")
	key := require(t, "LINGUAFLOW_TEST_S3_ACCESS_KEY_ID")
	secret := require(t, "LINGUAFLOW_TEST_S3_SECRET_ACCESS_KEY")
	region := require(t, "LINGUAFLOW_TEST_S3_REGION")
	options := s3store.Options{Endpoint: os.Getenv("LINGUAFLOW_TEST_S3_ENDPOINT"), Region: region, Bucket: bucket, Prefix: strings.TrimSuffix(prefix, "/") + "/" + suffix(t), PathStyle: os.Getenv("LINGUAFLOW_TEST_S3_PATH_STYLE") == "true", Credentials: credentials(key, secret, os.Getenv("LINGUAFLOW_TEST_S3_SESSION_TOKEN"))}
	store, err := s3store.New(options)
	if err != nil {
		t.Fatal("invalid S3 acceptance configuration")
	}
	fixture := &S3{Store: store, Options: options, keys: map[string]bool{}}
	t.Cleanup(func() { fixture.cleanup(t) })
	return fixture
}

func credentials(key, secret, token string) aws.CredentialsProvider {
	return aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: key, SecretAccessKey: secret, SessionToken: token}, nil
	})
}

func (s *S3) Track(key string) {
	if storeutil.ValidateKey(key) != nil {
		return
	}
	s.mu.Lock()
	s.keys[key] = true
	s.mu.Unlock()
}
func (s *S3) PutNew(ctx context.Context, key string, r io.Reader, size int64) (storage.Object, error) {
	s.Track(key)
	return s.Store.PutNew(ctx, key, r, size)
}

// Profile uses the same isolated prefix with optional restricted/revoked credentials.
func (s *S3) Profile(t *testing.T, name string) *s3store.Store {
	t.Helper()
	options := s.Options
	key := require(t, "LINGUAFLOW_TEST_S3_"+name+"_ACCESS_KEY_ID")
	secret := require(t, "LINGUAFLOW_TEST_S3_"+name+"_SECRET_ACCESS_KEY")
	options.Credentials = credentials(key, secret, os.Getenv("LINGUAFLOW_TEST_S3_"+name+"_SESSION_TOKEN"))
	result, err := s3store.New(options)
	if err != nil {
		t.Fatal("invalid restricted S3 profile")
	}
	return result
}

func (s *S3) cleanup(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	caps, err := s.Capabilities(ctx)
	if err != nil {
		t.Error("cannot determine exact S3 cleanup mode; registered test objects remain")
		return
	}
	s.mu.Lock()
	keys := make([]string, 0, len(s.keys))
	for key := range s.keys {
		keys = append(keys, key)
	}
	s.mu.Unlock()
	for _, key := range keys {
		if caps.Versioned {
			versions, err := s.Versions(ctx, key)
			if err != nil {
				t.Error("cannot list registered test key versions for cleanup")
				continue
			}
			for _, object := range versions {
				if err = s.Store.Delete(ctx, object); err != nil {
					t.Error("cannot delete registered test object version")
				}
			}
		} else {
			if err = s.Store.Delete(ctx, storage.Object{Key: key}); err != nil {
				t.Error("cannot delete registered test object")
			}
		}
	}
}
