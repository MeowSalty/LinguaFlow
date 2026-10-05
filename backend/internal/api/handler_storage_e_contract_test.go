package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storageconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/storagespace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storageauth"
)

func storageERequest(t *testing.T, router http.Handler, actor *ent.User, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if actor != nil {
		req = withAuthUser(req, actor)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func assertStorageEProblem(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var problem problemDetails
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("problem JSON: %v (%s)", err, response.Body)
	}
	if response.Code != status || problem.ErrorCode != code {
		t.Fatalf("HTTP %d %+v; want %d %s", response.Code, problem, status, code)
	}
	if response.Header().Get("Retry-After") != "" {
		t.Fatal("state conflict instructed automatic retry")
	}
	assertStorageSchema(t, "Problem", response.Body.Bytes())
}

func TestStorageECapabilitiesHTTPContractAndScope(t *testing.T) {
	s, client, owner, _, keys := storageAPIServer(t)
	s.closeStorageResources()
	s.serverCfg.Storage.Enabled = false
	if err := s.initStorage(context.Background(), keys); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	users := service.NewUserService(client, nil)
	org, err := users.CreateOrganization(ctx, owner.ID, service.CreateOrganizationInput{Name: "Storage E", Slug: "storage-e"})
	if err != nil {
		t.Fatal(err)
	}
	member := client.User.Create().SetUsername("e-member").SetEmail("e-member@example.test").SetPasswordHash("unused").SaveX(ctx)
	outsider := client.User.Create().SetUsername("e-admin-outsider").SetEmail("e-admin@example.test").SetPasswordHash("unused").SetRole(service.SystemRoleAdmin).SaveX(ctx)
	role := service.OrgRoleMember
	if _, err := users.AddMember(ctx, owner.ID, org.ID, service.AddOrgMemberInput{Username: member.Username, Role: &role}); err != nil {
		t.Fatal(err)
	}
	router := s.newRouter()
	for _, scope := range []string{"user", "org"} {
		path := "/storage/capabilities?scope=" + scope
		wantOwner := owner.ID
		if scope == "org" {
			path += fmt.Sprintf("&organization_id=%d", org.ID)
			wantOwner = org.ID
		}
		w := storageERequest(t, router, owner, "GET", path, "")
		var got service.StorageCapabilities
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || got.Scope != scope || got.OwnerID != wantOwner || got.Runtime.DeploymentEnabled || got.ManagementActions.CreateConnection.Allowed || !slices.Equal(got.ManagementActions.CreateConnection.ReasonCodes, []string{"storage_deployment_disabled"}) {
			t.Fatalf("disabled empty capability: %d %s", w.Code, w.Body)
		}
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("owner-specific capability response may be shared or cached")
		}
		assertStorageSchema(t, "StorageCapabilities", w.Body.Bytes())
	}
	for _, actor := range []*ent.User{member, outsider} {
		w := storageERequest(t, router, actor, "GET", fmt.Sprintf("/storage/capabilities?scope=org&organization_id=%d", org.ID), "")
		assertStorageEProblem(t, w, 403, "forbidden")
	}
	for _, query := range []string{"", "scope=site", "scope=user&owner_id=99", "scope=user&organization_id=1", "scope=org", "scope=org&organization_id=0", "scope=user&scope=org", "scope=", "scope=user&unknown=1"} {
		w := storageERequest(t, router, owner, "GET", "/storage/capabilities?"+query, "")
		if w.Code != 400 {
			t.Fatalf("invalid discovery query %q: %d %s", query, w.Code, w.Body)
		}
	}
	assertStorageEProblem(t, storageERequest(t, router, nil, "GET", "/storage/capabilities?scope=user", ""), 401, "unauthorized")
	assertStorageEProblem(t, storageERequest(t, router, owner, "GET", "/admin/storage/policy", ""), 403, "forbidden")
	assertStorageEProblem(t, storageERequest(t, router, owner, "GET", "/admin/storage/connections", ""), 403, "forbidden")
}

func TestStorageEPolicyResponseCannotBeWrittenBack(t *testing.T) {
	s, client, owner, _, _ := storageAPIServer(t)
	owner = client.User.UpdateOneID(owner.ID).SetRole(service.SystemRoleAdmin).SaveX(context.Background())
	router := s.newRouter()
	read := storageERequest(t, router, owner, "GET", "/admin/storage/policy", "")
	if read.Code != 200 {
		t.Fatalf("policy GET: %d %s", read.Code, read.Body)
	}
	assertStorageSchema(t, "StoragePolicy", read.Body.Bytes())
	assertStorageEProblem(t, storageERequest(t, router, owner, "PUT", "/admin/storage/policy", read.Body.String()), 400, "invalid_input")
	for _, field := range []string{`"runtime":{"deployment_enabled":true,"maintenance":false}`, `"allowed_policy_modes":["site_only"]`, `"policy_restriction_codes":[]`, `"configuration_needs_update":false`} {
		body := `{"mode":"site_only","default_choice":"site","generation":0,"logical_limit_bytes":12345,` + field + `}`
		assertStorageEProblem(t, storageERequest(t, router, owner, "PUT", "/admin/storage/policy", body), 400, "invalid_input")
	}
	policy, err := s.storageSvc.Policy(context.Background())
	if err != nil || policy.Generation != 0 {
		t.Fatalf("rejected DTO changed policy: %+v %v", policy, err)
	}
	accepted := storageERequest(t, router, owner, "PUT", "/admin/storage/policy", `{"mode":"site_only","default_choice":"site","generation":0,"logical_limit_bytes":12345}`)
	if accepted.Code != 200 {
		t.Fatalf("independent request DTO: %d %s", accepted.Code, accepted.Body)
	}
	assertStorageSchema(t, "StoragePolicy", accepted.Body.Bytes())
}

type storageEDriver struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
	deletes int
}

func (d *storageEDriver) PutNew(_ context.Context, key string, r io.Reader, size int64) (storage.Object, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return storage.Object{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.puts++
	if _, ok := d.objects[key]; ok {
		return storage.Object{}, storage.ErrExists
	}
	if int64(len(data)) != size {
		return storage.Object{}, storage.ErrCorrupt
	}
	d.objects[key] = data
	return storage.Object{Key: key, Size: size}, nil
}

func (d *storageEDriver) Open(_ context.Context, object storage.Object) (io.ReadCloser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	data, ok := d.objects[object.Key]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(bytes.Clone(data))), nil
}

func (d *storageEDriver) Stat(_ context.Context, object storage.Object) (storage.Object, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	data, ok := d.objects[object.Key]
	if !ok {
		return storage.Object{}, storage.ErrNotFound
	}
	return storage.Object{Key: object.Key, Size: int64(len(data))}, nil
}

func (d *storageEDriver) Delete(_ context.Context, object storage.Object) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deletes++
	delete(d.objects, object.Key)
	return nil
}

func (d *storageEDriver) Capabilities(context.Context) (storage.Capabilities, error) {
	return storage.Capabilities{ConditionalCreate: true}, nil
}

func TestStorageEFullRouterManagementMaintenanceMatrix(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, barrier := range []string{"none", "configured", "offline"} {
			t.Run(fmt.Sprintf("enabled=%v/barrier=%s", enabled, barrier), func(t *testing.T) {
				s, client, owner, project, keys := storageAPIServer(t)
				ctx := context.Background()
				owner = client.User.UpdateOneID(owner.ID).SetRole(service.SystemRoleAdmin).SaveX(ctx)
				driver := &storageEDriver{objects: map[string][]byte{}}
				factory := func(context.Context, *ent.StorageConnection, *ent.StorageSpace, storageauth.S3Payload) (storage.Driver, error) {
					return driver, nil
				}
				connections := service.NewStorageConnectionService(client, keys, s.serverCfg.Storage, factory)
				conn, err := connections.Create(ctx, owner.ID, service.CreateStorageConnectionInput{Name: "managed", Scope: "user", OwnerID: owner.ID, Endpoint: "https://storage.example", Region: "test"})
				if err != nil {
					t.Fatal(err)
				}
				space, err := connections.CreateSpace(ctx, owner.ID, conn.ID, service.CreateStorageSpaceInput{Name: "managed", Bucket: "test-bucket", Prefix: "existing", CapacityBytes: 1 << 20})
				if err != nil {
					t.Fatal(err)
				}
				generation := func() int64 { return client.StorageConnection.GetX(ctx, conn.ID).ManagementGeneration }
				if _, err := connections.Authorize(ctx, owner.ID, conn.ID, service.AuthorizeStorageInput{Payload: storageauth.S3Payload{Version: 1, AccessKeyID: "access", SecretAccessKey: "secret"}, WriteCheck: true, ExpectedManagementGeneration: generation()}); err != nil {
					t.Fatal(err)
				}
				s.closeStorageResources()
				s.serverCfg.Storage.Enabled = enabled
				s.serverCfg.Storage.Maintenance = barrier == "configured"
				if barrier == "offline" {
					client.StorageTask.Create().SetOperationID("offline-e").SetIdempotencyKey("offline-e").SetRequestHash("offline-manifest").SetKind("legacy_migration").SetPhase("applying").SetStatus("needs_action").ExecX(ctx)
					client.Project.UpdateOneID(project.ID).SetStorageState("legacy_migration").ExecX(ctx)
				}
				if err := s.initStorage(ctx, keys); err != nil {
					t.Fatal(err)
				}
				maintenance := barrier != "none"
				if s.storageSvc.Runtime().Maintenance != maintenance || s.storageConnections.Runtime().Maintenance != maintenance {
					t.Fatal("startup recovery did not reach both runtime projections")
				}
				cfg := s.serverCfg.Storage
				cfg.Maintenance = s.storageConnections.Runtime().Maintenance
				s.storageConnections = service.NewStorageConnectionService(client, keys, cfg, factory)
				router := s.newRouter()
				for _, tc := range []struct{ path, schema string }{{"/storage/capabilities?scope=user", "StorageCapabilities"}, {"/admin/storage/policy", "StoragePolicy"}, {"/storage/options?scope=user", "StorageOptions"}, {fmt.Sprintf("/projects/%d/storage", project.ID), "ProjectStorage"}} {
					w := storageERequest(t, router, owner, "GET", tc.path, "")
					if w.Code != 200 {
						t.Fatalf("maintenance metadata GET: %d %s", w.Code, w.Body)
					}
					assertStorageSchema(t, tc.schema, w.Body.Bytes())
					var projection struct {
						Runtime service.StorageRuntime `json:"runtime"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &projection); err != nil || projection.Runtime.Maintenance != maintenance || projection.Runtime.DeploymentEnabled != enabled {
						t.Fatalf("runtime projection: %s %v", w.Body, err)
					}
				}
				policy := storageERequest(t, router, owner, "PUT", "/admin/storage/policy", `{"mode":"site_only","default_choice":"site","generation":0,"logical_limit_bytes":12345}`)
				if policy.Code != 200 {
					t.Fatalf("maintenance blocked legal policy save: %d %s", policy.Code, policy.Body)
				}
				reason := "storage_deployment_disabled"
				if maintenance {
					reason = "storage_maintenance"
				}
				for _, authorize := range []bool{false, true} {
					for _, write := range []bool{false, true} {
						path := fmt.Sprintf("/storage/connections/%d/check", conn.ID)
						body := fmt.Sprintf(`{"write_check":%v,"expected_generation":%d}`, write, generation())
						if authorize {
							path = fmt.Sprintf("/storage/connections/%d/authorize", conn.ID)
							body = fmt.Sprintf(`{"access_key_id":"access","secret_access_key":"secret","write_check":%v,"expected_management_generation":%d}`, write, generation())
						}
						puts, deletes := driver.puts, driver.deletes
						w := storageERequest(t, router, owner, "POST", path, body)
						allowed := (!authorize && !write) || (!maintenance && (!write || enabled))
						if allowed {
							if w.Code != 200 {
								t.Fatalf("allowed management authorize=%v write=%v: %d %s", authorize, write, w.Code, w.Body)
							}
							assertStorageSchema(t, "StorageConnection", w.Body.Bytes())
						} else {
							assertStorageEProblem(t, w, 409, reason)
						}
						if (!write || !allowed) && (driver.puts != puts || driver.deletes != deletes) {
							t.Fatal("readonly or rejected HTTP management request wrote remotely")
						}
						if !allowed {
							var problem problemDetails
							if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
								t.Fatal(err)
							}
							if problem.CheckID != 0 {
								check := storageERequest(t, router, owner, "GET", fmt.Sprintf("/storage/connections/%d/checks/%d", conn.ID, problem.CheckID), "")
								if check.Code != 200 || !strings.Contains(check.Body.String(), `"error_code":"`+reason+`"`) {
									t.Fatalf("check lost deployment error: %d %s", check.Code, check.Body)
								}
								assertStorageSchema(t, "StorageCheck", check.Body.Bytes())
							}
						}
					}
				}
				for _, status := range []string{"disabled", "enabled"} {
					w := storageERequest(t, router, owner, "PATCH", fmt.Sprintf("/storage/connections/%d", conn.ID), fmt.Sprintf(`{"status":%q,"expected_generation":%d}`, status, generation()))
					if w.Code != 200 {
						t.Fatalf("management status blocked: %d %s", w.Code, w.Body)
					}
				}
				for _, id := range []int{space.ID, *project.StorageSpaceID} {
					row := client.StorageSpace.GetX(ctx, id)
					w := storageERequest(t, router, owner, "PATCH", fmt.Sprintf("/storage/spaces/%d", id), fmt.Sprintf(`{"status":"read_only","expected_generation":%d}`, row.ManagementGeneration))
					if w.Code != 200 {
						t.Fatalf("space status blocked: %d %s", w.Code, w.Body)
					}
					assertStorageSchema(t, "StorageSpace", w.Body.Bytes())
				}
				w := storageERequest(t, router, owner, "POST", fmt.Sprintf("/storage/connections/%d/revoke", conn.ID), fmt.Sprintf(`{"expected_generation":%d}`, generation()))
				if w.Code != 200 {
					t.Fatalf("maintenance blocked revoke: %d %s", w.Code, w.Body)
				}
				for _, tc := range []struct{ path, body string }{{"/storage/connections", `{"name":"new","endpoint":"https://new.example","region":"test"}`}, {fmt.Sprintf("/storage/connections/%d/spaces", conn.ID), `{"name":"new","bucket":"other-bucket","prefix":"new","capacity_bytes":1000}`}} {
					w := storageERequest(t, router, owner, "POST", tc.path, tc.body)
					if !enabled || maintenance {
						assertStorageEProblem(t, w, 409, reason)
					} else if w.Code != 201 {
						t.Fatalf("enabled creation: %d %s", w.Code, w.Body)
					}
				}
				if maintenance {
					assertStorageEProblem(t, storageERequest(t, router, owner, "POST", "/projects", `{"name":"blocked"}`), 409, "storage_maintenance")
					assertStorageEProblem(t, storageERequest(t, router, owner, "DELETE", fmt.Sprintf("/projects/%d", project.ID), ""), 409, "storage_maintenance")
					assertStorageEProblem(t, storageERequest(t, router, owner, "PATCH", fmt.Sprintf("/projects/%d/resources/1/segments/1", project.ID), `{"target_text":"blocked"}`), 409, "storage_maintenance")
					for _, action := range []string{"retry", "cancel"} {
						assertStorageEProblem(t, storageERequest(t, router, owner, "POST", fmt.Sprintf("/projects/%d/storage/tasks/1/%s", project.ID, action), "{}"), 409, "storage_maintenance")
					}
					assertStorageEProblem(t, storageERequest(t, router, nil, "POST", "/storage/connections", `{"name":"new","endpoint":"https://new.example","region":"test"}`), 401, "unauthorized")
					users := service.NewUserService(client, nil)
					org, err := users.CreateOrganization(ctx, owner.ID, service.CreateOrganizationInput{Name: "Maintenance", Slug: "maintenance"})
					if err != nil {
						t.Fatal(err)
					}
					orgPath := fmt.Sprintf("/orgs/%d/storage/connections", org.ID)
					body := `{"name":"new","endpoint":"https://new.example","region":"test"}`
					assertStorageEProblem(t, storageERequest(t, router, owner, "POST", orgPath, body), 409, "storage_maintenance")
					for _, membership := range []string{service.OrgRoleAdmin, service.OrgRoleMember, "outsider"} {
						actor := client.User.Create().SetUsername("e-" + membership).SetEmail(membership + "@example.test").SetPasswordHash("unused").SaveX(ctx)
						if membership != "outsider" {
							if _, err := users.AddMember(ctx, owner.ID, org.ID, service.AddOrgMemberInput{Username: actor.Username, Role: &membership}); err != nil {
								t.Fatal(err)
							}
						}
						status, code := 403, "forbidden"
						if membership == service.OrgRoleAdmin {
							status, code = 409, "storage_maintenance"
						}
						assertStorageEProblem(t, storageERequest(t, router, actor, "POST", orgPath, body), status, code)
						if membership == "outsider" {
							assertStorageEProblem(t, storageERequest(t, router, actor, "POST", fmt.Sprintf("/storage/connections/%d/check", conn.ID), fmt.Sprintf(`{"write_check":true,"expected_generation":%d}`, generation())), 403, "forbidden")
							assertStorageEProblem(t, storageERequest(t, router, actor, "PATCH", fmt.Sprintf("/storage/connections/%d", conn.ID), fmt.Sprintf(`{"status":"disabled","expected_generation":%d}`, generation())), 403, "forbidden")
						}
					}
				}
			})
		}
	}
}

func TestStorageEHTTPDeploymentErrorIdentity(t *testing.T) {
	s, client, owner, project, _ := storageAPIServer(t)
	row := client.StorageTask.Create().SetProjectID(project.ID).SetActorID(owner.ID).SetOperationID("deployment-blocked").SetIdempotencyKey("deployment-blocked").SetRequestHash("deployment-request").SetKind("upload").SetPhase("accepted").SetStatus("needs_action").SetErrorCode("storage_deployment_disabled").SetTargetSpaceID(*project.StorageSpaceID).SaveX(context.Background())
	client.StorageSpace.UpdateOneID(*project.StorageSpaceID).SetStatus(storagespace.StatusReadOnly).ExecX(context.Background())
	w := httptest.NewRecorder()
	s.writeStorageError(w, httptest.NewRequest("POST", "/task", nil), &service.StorageOperationError{Err: service.ErrStorageDeploymentDisabled, TaskID: row.ID, OperationID: row.OperationID})
	assertStorageEProblem(t, w, 409, "storage_deployment_disabled")
	var problem problemDetails
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil || problem.TaskID != row.ID || problem.OperationID != row.OperationID {
		t.Fatalf("durable error identity lost: %+v %v", problem, err)
	}
	response := storageERequest(t, s.newRouter(), owner, "GET", fmt.Sprintf("/projects/%d/storage/tasks/%d", project.ID, row.ID), "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"error_code":"storage_deployment_disabled"`) || strings.Contains(response.Body.String(), `"retry"`) {
		t.Fatalf("task projection lost deployment rejection: %d %s", response.Code, response.Body)
	}
	assertStorageSchema(t, "StorageTask", response.Body.Bytes())
}

func TestStorageEHTTPBatchKeepsDeploymentError(t *testing.T) {
	s, client, owner, project, keys := storageAPIServer(t)
	s.closeStorageResources()
	s.serverCfg.Storage.Enabled = false
	if err := s.initStorage(context.Background(), keys); err != nil {
		t.Fatal(err)
	}
	space := client.StorageSpace.GetX(context.Background(), *project.StorageSpaceID)
	client.StorageConnection.UpdateOneID(space.ConnectionID).SetDriver(storageconnection.DriverS3).ExecX(context.Background())
	body := new(bytes.Buffer)
	form := multipart.NewWriter(body)
	part, err := form.CreateFormFile("files", "one.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "one"); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := withAuthUser(httptest.NewRequest("POST", fmt.Sprintf("/api/v1/projects/%d/resources", project.ID), body), owner)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Idempotency-Key", "disabled-http-batch")
	w := httptest.NewRecorder()
	s.newRouter().ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"error_code":"storage_deployment_disabled"`) || strings.Contains(w.Body.String(), "storage_unavailable") || w.Header().Get("Retry-After") != "" {
		t.Fatalf("batch HTTP lost deployment failure: %d %s", w.Code, w.Body)
	}
	assertStorageSchema(t, "ResourceUploadBatchResponse", w.Body.Bytes())
}
