package s3store_test

import (
	"bytes"
	"context"
	"errors"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageacceptance"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/s3store"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type loseSuccessfulPut struct {
	client *http.Client
	lost   bool
	puts   int
}

func (c *loseSuccessfulPut) Do(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPut {
		c.puts++
	}
	response, err := c.client.Do(r)
	if err == nil && !c.lost && r.Method == http.MethodPut && response.StatusCode >= 200 && response.StatusCode < 300 {
		c.lost = true
		_ = response.Body.Close()
		return nil, errors.New("acceptance discarded successful PUT response")
	}
	return response, err
}

func TestStorageDeploymentS3ProviderContract(t *testing.T) {
	d := storageacceptance.NewS3(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	caps, err := d.Capabilities(ctx)
	if err != nil {
		t.Fatalf("capability inspection: %v", err)
	}
	if !caps.ConditionalCreate || (caps.Versioned && !caps.ExactVersions) {
		t.Fatal("provider lacks required immutable object capabilities")
	}
	payload := []byte("verified original\x00\xff\n")
	object, err := d.PutNew(ctx, "original", bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.PutNew(ctx, "original", strings.NewReader("replace"), 7); !errors.Is(err, storage.ErrExists) {
		t.Fatalf("conditional create: %v", err)
	}
	reader, err := d.Open(ctx, object)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatal("provider roundtrip bytes changed")
	}
	other, err := d.PutNew(ctx, "independent", strings.NewReader("safe"), 4)
	if err != nil {
		t.Fatal(err)
	}
	if caps.Versioned && object.Version == "" {
		t.Fatal("versioned provider omitted immutable version identity")
	}
	if err = d.Delete(ctx, object); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Stat(ctx, object); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("exact deletion not confirmed: %v", err)
	}
	if _, err = d.Stat(ctx, other); err != nil {
		t.Fatal("deletion affected independent object")
	}
	t.Run("lost-successful-write-response", func(t *testing.T) {
		transport := &loseSuccessfulPut{client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
		options := d.Options
		options.HTTPClient = transport
		uncertain, e := s3store.New(options)
		if e != nil {
			t.Fatal(e)
		}
		d.Track("response-lost")
		if _, e = uncertain.PutNew(ctx, "response-lost", strings.NewReader("persisted"), 9); !errors.Is(e, storage.ErrUnavailable) {
			t.Fatalf("response loss classification: %v", e)
		}
		if !transport.lost || transport.puts != 1 {
			t.Fatal("provider write was not sent exactly once")
		}
		observed, e := d.Stat(ctx, storage.Object{Key: "response-lost"})
		if e != nil || observed.Size != 9 {
			t.Fatal("lost response did not retain the actual provider object")
		}
	})
	t.Run("read-only-provider-permissions", func(t *testing.T) {
		restricted := d.Profile(t, "READONLY")
		if _, err := restricted.Stat(ctx, other); err != nil {
			t.Fatalf("read-only profile cannot read test object: %v", err)
		}
		d.Track("denied-write")
		if _, err := restricted.PutNew(ctx, "denied-write", strings.NewReader("x"), 1); !errors.Is(err, storage.ErrPermission) {
			t.Fatalf("read-only provider write mapping: %v", err)
		}
		if err := restricted.Delete(ctx, other); !errors.Is(err, storage.ErrPermission) {
			t.Fatalf("read-only provider deletion mapping: %v", err)
		}
		if _, err := d.Stat(ctx, other); err != nil {
			t.Fatal("denied provider deletion lost the object")
		}
	})
	t.Run("revoked-provider-credentials", func(t *testing.T) {
		revoked := d.Profile(t, "REVOKED")
		if _, err := revoked.Stat(ctx, other); !errors.Is(err, storage.ErrAuthRequired) && !errors.Is(err, storage.ErrPermission) {
			t.Fatalf("revoked provider credentials mapping: %v", err)
		}
	})
}
