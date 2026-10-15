package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"context"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
)

func TestStorageRetirementErrorDirectory(t *testing.T) {
	s, _, _, _, _ := storageAPIServer(t)
	for _, tc := range []struct {
		err    error
		code   string
		status int
	}{
		{service.ErrStorageRetiring, "storage_retiring", 409},
		{service.ErrStorageRemoved, "storage_removed", 410},
		{service.ErrStorageRetirementConflict, "storage_retirement_conflict", 409},
		{service.ErrStorageRetirementPreviewStale, "storage_retirement_preview_stale", 409},
		{service.ErrStorageRetirementDependenciesRemaining, "storage_retirement_dependencies_remaining", 409},
		{service.ErrStorageRetirementConfigurationPending, "storage_retirement_configuration_pending", 409},
	} {
		t.Run(tc.code, func(t *testing.T) {
			w := httptest.NewRecorder()
			wrapped := fmt.Errorf("internal provider detail must stay private: %w", tc.err)
			s.writeStorageError(w, httptest.NewRequest("GET", "/retirement", nil), &service.StorageOperationError{Err: wrapped, RetirementID: 29})
			var problem problemDetails
			if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || problem.ErrorCode != tc.code || problem.RetirementID != 29 || problem.TaskID != 0 || service.StorageErrorCode(wrapped) != tc.code {
				t.Fatalf("retirement error projection: %d %+v", w.Code, problem)
			}
			if strings.Contains(w.Body.String(), "provider detail") || !errors.Is(wrapped, tc.err) {
				t.Fatal("domain wrapping lost identity or leaked internal detail")
			}
			assertStorageSchema(t, "Problem", w.Body.Bytes())
		})
	}
}

func TestStorageRetirementDiscoveryContract(t *testing.T) {
	s, client, owner, _, _ := storageAPIServer(t)
	ctx := t.Context()
	conn := client.StorageConnection.Create().SetName("managed").SetDriver("s3").SetOwnerKind("user").SetOwnerID(owner.ID).SetAuthSource("stored").SaveX(ctx)
	client.StorageSpace.Create().SetConnectionID(conn.ID).SetName("managed").SetIdentity("retirement-contract").SetMarkerNonce("retirement-marker").SetOwnerKind("user").SetOwnerID(owner.ID).SaveX(ctx)
	router := Handler(s)
	for _, tc := range []struct{ path, schema, capability string }{
		{"/storage/capabilities?scope=user", "StorageCapabilities", `"contract_version":1` + " " + `"unused_removal":{"allowed":true` + " " + `"in_use_retirement":{"allowed":true`},
		{"/storage/connections", "StorageConnectionList", `"retire":{"allowed":true`},
		{fmt.Sprintf("/storage/connections/%d/spaces", conn.ID), "StorageSpaceList", `"retire":{"allowed":true`},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, withAuthUser(httptest.NewRequest("GET", tc.path, nil), owner))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
		assertStorageSchema(t, tc.schema, w.Body.Bytes())
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s: retirement discovery must stay private", tc.path)
		}
		for _, part := range strings.Split(tc.capability, " ") {
			if !strings.Contains(w.Body.String(), part) {
				t.Fatalf("%s: retirement discovery missing open capability %s: %s", tc.path, part, w.Body)
			}
		}
	}
}

func TestStorageRetirementRoutesRequireAuthenticationAndValidInput(t *testing.T) {
	s, client, owner, _, keys := storageAPIServer(t)
	// Fail the test at provider construction, before even a read-only probe.
	s.storageConnections = service.NewStorageConnectionService(client, keys, s.serverCfg.Storage,
		func(context.Context, *ent.StorageConnection, *ent.StorageSpace, storageauth.S3Payload) (storage.Driver, error) {
			t.Error("retirement gate reached a provider")
			return nil, storage.ErrUnsupported
		})
	ctx := t.Context()
	counts := func() map[string]int {
		return map[string]int{
			"connections":            client.StorageConnection.Query().CountX(ctx),
			"spaces":                 client.StorageSpace.Query().CountX(ctx),
			"retirements":            client.StorageRetirement.Query().CountX(ctx),
			"entries":                client.StorageRetirementEntry.Query().CountX(ctx),
			"tasks":                  client.StorageTask.Query().CountX(ctx),
			"writes":                 client.StorageWrite.Query().CountX(ctx),
			"checks":                 client.StorageCheck.Query().CountX(ctx),
			"authorization_versions": client.StorageAuthVersion.Query().CountX(ctx),
		}
	}
	before := counts()
	for _, maintenance := range []bool{false, true} {
		s.storageRuntime.maintenance = maintenance
		router := s.newRouter()
		for _, tc := range []struct {
			method, path      string
			authStatus        int
			authCode          string
			authProblemSchema bool
		}{
			{"POST", "/storage/retirement-previews", 400, "invalid_input", true},
			{"POST", "/storage/retirements", 400, "invalid_input", true},
			{"GET", fmt.Sprintf("/storage/retirements?scope=user&owner_id=%d", owner.ID), 200, "", false},
			{"GET", "/storage/retirements/1", 404, "not_found", true},
			{"PATCH", "/storage/retirements/1/plan", 400, "invalid_input", true},
			{"POST", "/storage/retirements/1/resume", 400, "invalid_input", true},
			{"POST", "/storage/retirements/1/cancel", 400, "invalid_input", true},
			{"POST", "/storage/retirements/1/recover-authorization", 400, "invalid_input", true},
		} {
			for _, authenticated := range []bool{false, true} {
				req := httptest.NewRequest(tc.method, "/api/v1"+tc.path, strings.NewReader(`{"secret_access_key":"must-not-be-read-or-logged"}`))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Idempotency-Key", "retirement-disabled")
				if authenticated {
					req = withAuthUser(req, owner)
				}
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				wantStatus, wantCode := 401, "unauthorized"
				if authenticated {
					wantStatus, wantCode = tc.authStatus, tc.authCode
				}
				if w.Code != wantStatus {
					t.Fatalf("%s %s maintenance=%v authenticated=%v: %d %s", tc.method, tc.path, maintenance, authenticated, w.Code, w.Body)
				}
				if wantCode == "" {
					continue
				}
				var problem problemDetails
				if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil || problem.ErrorCode != wantCode {
					t.Fatalf("%s %s maintenance=%v authenticated=%v: %d %s", tc.method, tc.path, maintenance, authenticated, w.Code, w.Body)
				}
				assertStorageSchema(t, "Problem", w.Body.Bytes())
			}
		}
	}
	if after := counts(); !reflect.DeepEqual(before, after) {
		t.Fatalf("disabled retirement endpoint mutated persistent storage state: before=%v after=%v", before, after)
	}
}

func TestStorageRetirementHTTPPreviewRecoveryCompletionAndReplay(t *testing.T) {
	s, client, owner, _, _ := storageAPIServer(t)
	ctx := t.Context()
	conn, err := s.storageConnections.Create(ctx, owner.ID, service.CreateStorageConnectionInput{Name: "unused", Scope: "user", OwnerID: owner.ID, Endpoint: "https://s3.example", Region: "test"})
	if err != nil {
		t.Fatal(err)
	}
	sp, err := s.storageConnections.CreateSpace(ctx, owner.ID, conn.ID, service.CreateStorageSpaceInput{Name: "unused", Bucket: "unused-bucket", Prefix: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	router := s.newRouter()
	request := func(method, path, key string, body any) *httptest.ResponseRecorder {
		t.Helper()
		data, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(string(data)))
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, withAuthUser(req, owner))
		return w
	}
	w := request("POST", "/storage/retirement-previews", "", map[string]any{"target_kind": "space", "target_id": sp.ID, "plan": []any{}})
	if w.Code != 200 {
		t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
	assertStorageSchema(t, "StorageRetirementPreview", w.Body.Bytes())
	var preview service.RetirementPreview
	if err = json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	in := service.RetirementCreateInput{PreviewRevision: preview.PreviewRevision, ConfirmedScope: preview.Target, Plan: []service.RetirementProjectPlan{}}
	w = request("POST", "/storage/retirements", "http-retire", in)
	if w.Code != 202 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	assertStorageSchema(t, "StorageRetirement", w.Body.Bytes())
	location := w.Header().Get("Location")
	r := client.StorageRetirement.Query().OnlyX(ctx)
	if location != fmt.Sprintf("/api/v1/storage/retirements/%d", r.ID) {
		t.Fatal("missing stable task location")
	}
	if err = s.storageRetirements.ProcessOne(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r = client.StorageRetirement.GetX(ctx, r.ID)
	// Optional expiry is omitted, as in a normal browser credential form.
	w = request("POST", fmt.Sprintf("/storage/retirements/%d/recover-authorization", r.ID), "http-recover", map[string]any{"expected_generation": r.Generation, "expected_management_generation": client.StorageConnection.GetX(ctx, conn.ID).ManagementGeneration, "credentials": map[string]any{"access_key_id": "test-access", "secret_access_key": "test-secret"}})
	if w.Code != 200 {
		t.Fatalf("recovery without expiry: %d %s", w.Code, w.Body)
	}
	assertStorageSchema(t, "StorageRetirement", w.Body.Bytes())
	if err = s.storageRetirements.ProcessOne(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if client.StorageRetirement.GetX(ctx, r.ID).Status != "completed" {
		t.Fatal("retirement did not complete")
	}
	w = request("POST", "/storage/retirements", "http-retire", in)
	if w.Code != 202 || w.Header().Get("Location") != location || client.StorageRetirement.Query().CountX(ctx) != 1 {
		t.Fatalf("replay: %d %s", w.Code, w.Body)
	}
	w = request("GET", fmt.Sprintf("/storage/connections/%d/spaces?lifecycle=removed", conn.ID), "", nil)
	if w.Code != 200 {
		t.Fatalf("history: %d %s", w.Code, w.Body)
	}
	assertStorageSchema(t, "StorageSpaceList", w.Body.Bytes())
	if !strings.Contains(w.Body.String(), `"lifecycle":"removed"`) {
		t.Fatal("removed space missing from authorized history")
	}
	in.PreviewRevision += "modified"
	w = request("POST", "/storage/retirements", "http-retire", in)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "storage_idempotency_conflict") {
		t.Fatalf("changed replay: %d %s", w.Code, w.Body)
	}
}
