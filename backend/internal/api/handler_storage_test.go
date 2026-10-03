package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagetask"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/store/filestore"
)

func storageAPIServer(t *testing.T) (*Server, *ent.Client, *ent.User, *ent.Project, *credential.Keyring) {
	t.Helper()
	s, client, u := newTestServer(t)
	cfg := config.DefaultServerConfig()
	cfg.DataDir = t.TempDir()
	cfg.Storage.Enabled = true
	s.serverCfg = cfg
	keys, err := credential.ParseKeyring([]byte(`{"version":1,"active_key_id":"storage","keys":{"storage":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)) + `"}}`))
	if err != nil {
		t.Fatal(err)
	}
	s.projectSvc = service.NewProjectService(client, service.NewUserService(client, nil))
	files, err := filestore.NewLocal(filepath.Join(cfg.DataDir, "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	s.resourceSvc = service.NewResourceService(client, s.projectSvc, files)
	if err = s.initStorage(context.Background(), keys); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.closeStorageResources)
	sp := client.StorageSpace.Query().OnlyX(context.Background())
	p := client.Project.Create().SetName("storage-project").SetOwnerUserID(u.ID).SetStorageSpaceID(sp.ID).SaveX(context.Background())
	return s, client, u, p, keys
}

func TestStorageAPIStrictGenerationAndInput(t *testing.T) {
	s, client, u, p, _ := storageAPIServer(t)
	for _, body := range []string{`{"task_id":1}`, `{"task_id":1,"expected_source_generation":null,"expected_translation_generation":0}`, `{"task_id":1,"expected_source_generation":0,"expected_translation_generation":0,"task_id":2}`, `{"Task_ID":1,"expected_source_generation":0,"expected_translation_generation":0}`, `{"task_id":1,"expected_source_generation":0,"expected_translation_generation":0} {}`} {
		w := httptest.NewRecorder()
		s.CommitSourceUpdate(w, credentialAPIRequest(t, u, "POST", "/commit", body, nil), p.ID, 1)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid commit accepted: %d %s", w.Code, w.Body.String())
		}
	}
	for _, body := range []string{`{"kind":"migration","size":0,"idempotency_key":"x"}`, `{"kind":"upload","size":0,"idempotency_key":"x"}`, `{"kind":"upload","size":0,"path":"a.txt"}`} {
		w := httptest.NewRecorder()
		s.CreateStorageIntent(w, credentialAPIRequest(t, u, "POST", "/tasks", body, nil), p.ID)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid intent accepted: %d %s", w.Code, w.Body.String())
		}
	}
	if client.StorageTask.Query().Where(storagetask.ProjectIDEQ(p.ID)).CountX(context.Background()) != 0 {
		t.Fatal("invalid API input created durable tasks")
	}
}

func TestStorageAPIUploadTaskAndSafeProjection(t *testing.T) {
	s, client, u, p, _ := storageAPIServer(t)
	w := httptest.NewRecorder()
	s.CreateStorageIntent(w, credentialAPIRequest(t, u, "POST", "/tasks", `{"kind":"upload","size":5,"path":"hello.txt","idempotency_key":"client-upload","storage_generation":0}`, nil), p.ID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("intent: %d %s", w.Code, w.Body.String())
	}
	var task StorageTask
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"idempotency_key", "request_hash", "object_key", "input", "nonce", "ciphertext"} {
		if strings.Contains(w.Body.String(), `"`+secret+`"`) {
			t.Fatalf("task projection leaks %s", secret)
		}
	}
	req := withAuthUser(httptest.NewRequest("PUT", "/content", strings.NewReader("hello")), u)
	req.Header.Set("Content-Type", "application/octet-stream")
	w = httptest.NewRecorder()
	s.ReceiveStorageContent(w, req, p.ID, task.Id)
	if w.Code != http.StatusAccepted {
		t.Fatalf("receive: %d %s", w.Code, w.Body.String())
	}
	if err := s.storageSvc.ProcessTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.GetStorageTask(w, credentialAPIRequest(t, u, "GET", "/task", "", nil), p.ID, task.Id)
	if w.Code != http.StatusOK {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
		t.Fatal(err)
	}
	if task.Status != StorageTaskStatusCompleted || task.ResultResourceId == nil {
		t.Fatalf("upload not committed: %s", w.Body.String())
	}
	if client.Resource.Query().CountX(context.Background()) != 1 {
		t.Fatal("task did not publish one resource")
	}
	w = httptest.NewRecorder()
	s.GetProjectStorage(w, credentialAPIRequest(t, u, "GET", "/storage", "", nil), p.ID)
	if w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	for _, field := range []string{"endpoint", "bucket", "prefix", "marker_nonce", "identity"} {
		if strings.Contains(w.Body.String(), `"`+field+`"`) {
			t.Fatalf("project storage leaks %s", field)
		}
	}
}

func TestStorageAPIOwnershipSecretsAndAdminPolicy(t *testing.T) {
	s, client, u, p, _ := storageAPIServer(t)
	other := client.User.Create().SetUsername("other-storage").SetEmail("other-storage@example.test").SetPasswordHash("unused").SaveX(context.Background())
	w := httptest.NewRecorder()
	s.GetProjectStorage(w, credentialAPIRequest(t, other, "GET", "/storage", "", nil), p.ID)
	if w.Code != http.StatusForbidden {
		t.Fatalf("project scope escaped: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.ListSiteStorageConnections(w, credentialAPIRequest(t, u, "GET", "/admin/storage/connections", "", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("site listing was not admin-only: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.CreateStorageConnection(w, credentialAPIRequest(t, u, "POST", "/connections", `{"name":"other","scope":"user","owner_id":999,"endpoint":"https://s3.example","region":"test"}`, nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("caller-chosen owner accepted: %d", w.Code)
	}
	w = httptest.NewRecorder()
	s.AuthorizeStorage(w, credentialAPIRequest(t, u, "POST", "/authorize", `{"access_key_id":"secret-access","secret_access_key":"secret-key","write_check":true}`, nil), 1)
	if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "secret-key") {
		t.Fatalf("missing management generation/leaked secret: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.writeStorageError(w, httptest.NewRequest("GET", "/storage", nil), fmt.Errorf("provider says https://user:super-secret@example.invalid/?signature=private"))
	if strings.Contains(w.Body.String(), "super-secret") || strings.Contains(w.Body.String(), "signature") {
		t.Fatal("provider error exposed secret")
	}
}

func TestStorageAPITaskActionsFollowOrganizationRole(t *testing.T) {
	s, client, u, p, _ := storageAPIServer(t)
	ctx := context.Background()
	org := client.Organization.Create().SetName("storage-actions").SetSlug("storage-actions").SaveX(ctx)
	member := client.OrgMembership.Create().SetOrganizationID(org.ID).SetUserID(u.ID).SetRole(service.OrgRoleMember).SaveX(ctx)
	client.Project.UpdateOneID(p.ID).ClearOwnerUserID().SetOwnerOrgID(org.ID).ExecX(ctx)
	task := client.StorageTask.Create().SetOperationID("actions-operation").SetIdempotencyKey("actions-idempotency").SetRequestHash("actions-hash").SetProjectID(p.ID).SetKind("source_update").SetStatus("needs_action").SetPhase("prepared").SaveX(ctx)
	for _, role := range []string{service.OrgRoleMember, service.OrgRoleAdmin, service.OrgRoleMember} {
		client.OrgMembership.UpdateOneID(member.ID).SetRole(role).ExecX(ctx)
		for _, list := range []bool{false, true} {
			w := httptest.NewRecorder()
			r := credentialAPIRequest(t, u, "GET", "/tasks", "", nil)
			if list {
				s.ListStorageTasks(w, r, p.ID)
			} else {
				s.GetStorageTask(w, r, p.ID, task.ID)
			}
			if w.Code != http.StatusOK {
				t.Fatalf("read task as %s: %d %s", role, w.Code, w.Body.String())
			}
			var response StorageTask
			if list {
				var tasks StorageTaskList
				if err := json.Unmarshal(w.Body.Bytes(), &tasks); err != nil || len(tasks.Items) != 1 {
					t.Fatalf("task list: %s, %v", w.Body.String(), err)
				}
				response = tasks.Items[0]
			} else if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if role == service.OrgRoleMember && len(response.AllowedActions) != 0 {
				t.Fatalf("member received write actions: %v", response.AllowedActions)
			}
			if role == service.OrgRoleAdmin && len(response.AllowedActions) != 3 {
				t.Fatalf("admin missing cancel/retry/commit: %v", response.AllowedActions)
			}
		}
	}
}

func TestStorageSetupRejectsChangedLocalRootAndOverlappingRoots(t *testing.T) {
	s, _, _, _, keys := storageAPIServer(t)
	s.closeStorageResources()
	other := filepath.Join(s.serverCfg.DataDir, "replacement")
	s.serverCfg.Storage.Backends = []config.StorageBackendConfig{{ID: "local", Driver: "local", Root: other}}
	if err := s.initStorage(context.Background(), keys); !errors.Is(err, service.ErrStorageConflict) {
		t.Fatalf("local backend silently moved: %v", err)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatal("identity check created replacement root")
	}
	s.serverCfg.Storage.Backends = []config.StorageBackendConfig{{ID: "local", Driver: "local", Root: filepath.Join(s.serverCfg.DataDir, "objects")}, {ID: "nested", Driver: "local", Root: filepath.Join(s.serverCfg.DataDir, "objects", "nested")}}
	if err := s.initStorage(context.Background(), keys); !errors.Is(err, service.ErrStorageConflict) {
		t.Fatalf("overlapping roots accepted: %v", err)
	}
}

func TestStorageSetupHonorsPersistentOfflineMigrationBarrier(t *testing.T) {
	s, client, _, p, keys := storageAPIServer(t)
	s.closeStorageResources()
	ctx := context.Background()
	client.StorageTask.Create().SetOperationID("offline-operation").SetIdempotencyKey("offline-operation").SetRequestHash("manifest-hash").SetKind("legacy_migration").SetPhase("applying").SetStatus("needs_action").ExecX(ctx)
	client.Project.UpdateOneID(p.ID).SetStorageState("legacy_migration").ExecX(ctx)
	root := filepath.Join(s.serverCfg.DataDir, "objects")
	if err := os.Rename(root, root+".offline"); err != nil {
		t.Fatal(err)
	}
	if err := s.initStorage(ctx, keys); err != nil {
		t.Fatal(err)
	}
	if !s.storageMaintenance() {
		t.Fatal("persisted offline operation did not enable runtime maintenance")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("maintenance startup recreated a missing object root")
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, test := range []struct {
		method, path string
		want         int
	}{{"GET", "/api/v1/projects", http.StatusNoContent}, {"POST", "/api/v1/auth/login", http.StatusNoContent}, {"POST", "/api/v1/projects", http.StatusConflict}, {"PATCH", "/api/v1/projects/1/resources/1/segments/1", http.StatusConflict}} {
		w := httptest.NewRecorder()
		s.storageMaintenanceMiddleware(next).ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))
		if w.Code != test.want {
			t.Fatalf("maintenance %s %s: %d", test.method, test.path, w.Code)
		}
	}
}
