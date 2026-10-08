package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
)

func assertStorageSchema(t *testing.T, schema string, body []byte) {
	t.Helper()
	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	var value any
	if err = json.Unmarshal(body, &value); err != nil {
		t.Fatal(err)
	}
	ref := spec.Components.Schemas[schema]
	if ref == nil {
		t.Fatalf("missing schema %s", schema)
	}
	if err = ref.Value.VisitJSON(value); err != nil {
		t.Fatalf("%s response violates contract: %v\n%s", schema, err, body)
	}
}

func TestStorageContractDiscoveryAndRevoke(t *testing.T) {
	s, client, u, p, _ := storageAPIServer(t)
	handler := Handler(s)
	for _, tc := range []struct{ url, schema string }{{"/storage/options?scope=user", "StorageOptions"}, {fmt.Sprintf("/projects/%d/storage", p.ID), "ProjectStorage"}} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, withAuthUser(httptest.NewRequest("GET", tc.url, nil), u))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", tc.url, w.Code, w.Body)
		}
		assertStorageSchema(t, tc.schema, w.Body.Bytes())
		for _, secret := range []string{"capacity_bytes", "bucket", "endpoint", "reserved_bytes"} {
			if strings.Contains(w.Body.String(), `"`+secret+`"`) {
				t.Fatalf("discovery leaked %s", secret)
			}
		}
	}
	c := client.StorageConnection.Create().SetName("managed").SetDriver("s3").SetOwnerKind("user").SetOwnerID(u.ID).SetAuthSource("stored").SaveX(context.Background())
	for _, tc := range []struct {
		body string
		want int
	}{{`{"expected_generation":0,"status":"disabled"}`, 400}, {`{"expected_generation":0}`, 200}} {
		w := httptest.NewRecorder()
		s.RevokeStorageAuthorization(w, credentialAPIRequest(t, u, "POST", "/revoke", tc.body, nil), c.ID)
		if w.Code != tc.want {
			t.Fatalf("revoke: %d %s", w.Code, w.Body)
		}
	}
	current := client.StorageConnection.GetX(context.Background(), c.ID)
	if current.Status != "enabled" || current.ManagementGeneration != 1 {
		t.Fatalf("revoke changed management status: %+v", current)
	}
	check := client.StorageCheck.Create().SetConnectionID(c.ID).SetActorID(u.ID).SetMode("read_only").SetManagementGeneration(current.ManagementGeneration).SetResults(json.RawMessage(`[]`)).SaveX(context.Background())
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, withAuthUser(httptest.NewRequest("GET", fmt.Sprintf("/storage/connections/%d/checks/%d", c.ID, check.ID), nil), u))
	if w.Code != 200 {
		t.Fatalf("check: %d %s", w.Code, w.Body)
	}
	assertStorageSchema(t, "StorageCheck", w.Body.Bytes())
}

func TestStorageContractBatchReplayHTTP(t *testing.T) {
	s, client, u, p, _ := storageAPIServer(t)
	handler := Handler(s)
	send := func(names, contents []string, key string) *httptest.ResponseRecorder {
		t.Helper()
		body := new(bytes.Buffer)
		form := multipart.NewWriter(body)
		for i, name := range names {
			part, err := form.CreateFormFile("files", name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.WriteString(part, contents[i]); err != nil {
				t.Fatal(err)
			}
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := withAuthUser(httptest.NewRequest("POST", fmt.Sprintf("/projects/%d/resources", p.ID), body), u)
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	first := send([]string{"one.txt", "two.txt"}, []string{"first", "other"}, "batch-contract")
	if first.Code != 200 {
		t.Fatalf("upload: %d %s", first.Code, first.Body)
	}
	assertStorageSchema(t, "ResourceUploadBatchResponse", first.Body.Bytes())
	repeat := send([]string{"one.txt", "two.txt"}, []string{"first", "other"}, "batch-contract")
	if repeat.Code != 200 || repeat.Body.String() != first.Body.String() {
		t.Fatalf("replay changed result: %d %s", repeat.Code, repeat.Body)
	}
	for _, changed := range []*httptest.ResponseRecorder{send([]string{"one.txt", "two.txt"}, []string{"FIRST", "other"}, "batch-contract"), send([]string{"two.txt", "one.txt"}, []string{"other", "first"}, "batch-contract")} {
		if changed.Code != 409 || !strings.Contains(changed.Body.String(), `"error_code":"storage_idempotency_conflict"`) {
			t.Fatalf("manifest mismatch accepted: %d %s", changed.Code, changed.Body)
		}
	}
	if client.Resource.Query().CountX(context.Background()) != 2 || client.StorageUploadBatch.Query().CountX(context.Background()) != 1 {
		t.Fatal("replay duplicated persistent state")
	}
}

func TestStorageContractErrorDirectory(t *testing.T) {
	s, _, _, _, _ := storageAPIServer(t)
	for _, tc := range []struct {
		err    error
		code   string
		status int
	}{{service.ErrStorageIdempotency, "storage_idempotency_conflict", 409}, {service.ErrSourceRevisionConflict, "source_revision_conflict", 409}, {service.ErrStorageConflict, "storage_generation_conflict", 409}, {storage.ErrLimit, "storage_quota_exceeded", 409}, {storage.ErrPayloadTooLarge, "storage_payload_too_large", 413}, {context.DeadlineExceeded, "storage_timeout", 504}, {service.ErrStorageExpired, "storage_intent_expired", 409}} {
		w := httptest.NewRecorder()
		s.writeStorageError(w, httptest.NewRequest("GET", "/task", nil), &service.StorageOperationError{Err: tc.err, TaskID: 17})
		var problem problemDetails
		if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		if w.Code != tc.status || problem.ErrorCode != tc.code || problem.TaskID != 17 || service.StorageErrorCode(tc.err) != tc.code {
			t.Fatalf("inconsistent error: %d %+v", w.Code, problem)
		}
		assertStorageSchema(t, "Problem", w.Body.Bytes())
	}
}
