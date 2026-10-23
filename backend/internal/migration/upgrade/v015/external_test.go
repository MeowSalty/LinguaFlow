package v015

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestS3ReceiptRequiresExactBytesAndActualComplianceRetention(t *testing.T) {
	ctx := context.Background()
	payload := "exact retained bytes"
	sum := sha256.Sum256([]byte(payload))
	digest := hex.EncodeToString(sum[:])
	retain := time.Now().UTC().Add(48 * time.Hour)
	for _, test := range []struct {
		name, version, mode, bytes string
		retained                   time.Time
		pass                       bool
	}{
		{"compliance", "immutable-v1", "COMPLIANCE", payload, retain, true},
		{"wrong-version", "other-version", "COMPLIANCE", payload, retain, false},
		{"changed-bytes", "immutable-v1", "COMPLIANCE", "corrupt", retain, false},
		{"governance", "immutable-v1", "GOVERNANCE", payload, retain, false},
		{"expired", "immutable-v1", "COMPLIANCE", payload, time.Now().Add(-time.Hour), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/bucket/prefix/object.bin" || r.URL.Query().Get("versionId") != "immutable-v1" {
					t.Errorf("unexpected exact object request %s", r.URL.String())
					w.WriteHeader(400)
					return
				}
				if _, ok := r.URL.Query()["retention"]; ok {
					w.Header().Set("Content-Type", "application/xml")
					w.Write([]byte("<Retention><Mode>" + test.mode + "</Mode><RetainUntilDate>" + test.retained.Format(time.RFC3339) + "</RetainUntilDate></Retention>"))
					return
				}
				w.Header().Set("x-amz-version-id", test.version)
				w.Write([]byte(test.bytes))
			}))
			defer server.Close()
			client := s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(server.URL), UsePathStyle: true, Credentials: credentials.NewStaticCredentialsProvider("test-id", "test-secret", ""), HTTPClient: server.Client(), Retryer: aws.NopRetryer{}, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
			cfg := *config.DefaultServerConfig()
			cfg.DataDir = t.TempDir()
			err := verifyS3Recovery(ctx, client, "bucket", "prefix/object.bin", cfg, ExternalObject{Size: int64(len(payload)), SHA256: digest, ImmutableVersion: "immutable-v1", RetainUntil: time.Now().UTC().Add(24 * time.Hour), RetentionEvidence: "retention record"})
			if test.pass && err != nil {
				t.Fatal(err)
			}
			if !test.pass {
				var classified *operation.Error
				if !errors.As(err, &classified) || classified.Code != operation.BackupIncomplete {
					t.Fatalf("invalid retention accepted: %v", err)
				}
			}
		})
	}
	copy := filepath.Join(t.TempDir(), "recovery.bin")
	if _, err := operation.Publish(ctx, copy, strings.NewReader(payload)); err != nil {
		t.Fatal(err)
	}
	cfg := *config.DefaultServerConfig()
	cfg.DataDir = t.TempDir()
	evidence := ExternalObject{Size: int64(len(payload)), SHA256: digest, RecoveryFile: copy, RetainUntil: retain, RetentionEvidence: "separately retained copy"}
	if err := verifyS3Recovery(ctx, nil, "bucket", "object.bin", cfg, evidence); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copy, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyS3Recovery(ctx, nil, "bucket", "object.bin", cfg, evidence); err == nil {
		t.Fatal("changed recovery bytes accepted")
	}
	if err := VerifyExternalBackup(ctx, nil, cfg, nil, &operation.Plan{}, []BackupObject{{Driver: "s3", LocationID: 1}}); err == nil {
		t.Fatal("declaration without receipt accepted")
	}
}
