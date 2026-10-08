package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/diskspace"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/MeowSalty/LinguaFlow/backend/internal/storage"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStorageQuotaHTTPContract(t *testing.T) {
	s, client, u, p, _ := storageAPIServer(t)
	u = client.User.UpdateOne(u).SetRole(service.SystemRoleAdmin).SaveX(context.Background())
	id := *p.StorageSpaceID
	generation := client.StorageSpace.GetX(context.Background(), id).ManagementGeneration
	for _, body := range []string{`{}`, `{"capacity_bytes":null}`, `{"capacity_bytes":0,"expected_generation":0}`, `{"capacity_bytes":-1,"expected_generation":0}`, `{"capacity_bytes":9007199254740992,"expected_generation":0}`, `{"capacity_bytes":"null","expected_generation":0}`, `{"capacity_bytes":null,"expected_generation":null}`} {
		w := httptest.NewRecorder()
		s.SetStorageSpaceQuota(w, credentialAPIRequest(t, u, "PUT", "/quota", body, nil), id)
		if w.Code != 400 {
			t.Fatalf("invalid %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	for _, value := range []string{"1024", "null"} {
		w := httptest.NewRecorder()
		body := fmt.Sprintf(`{"capacity_bytes":%s,"expected_generation":%d}`, value, generation)
		s.SetStorageSpaceQuota(w, credentialAPIRequest(t, u, "PUT", "/quota", body, nil), id)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"capacity_bytes":`+value) {
			t.Fatalf("quota %s: %d %s", value, w.Code, w.Body.String())
		}
		stale := httptest.NewRecorder()
		s.SetStorageSpaceQuota(stale, credentialAPIRequest(t, u, "PUT", "/quota", body, nil), id)
		if stale.Code != 409 {
			t.Fatalf("stale CAS: %d", stale.Code)
		}
		var result StorageSpace
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if value == "null" {
			if result.AvailableBytes != nil {
				t.Fatalf("unlimited headroom: %s", w.Body.String())
			}
		} else if result.AvailableBytes == nil || *result.AvailableBytes != 1024-result.LiveBytes-result.ReservedBytes-result.CandidateBytes-result.PendingDeleteBytes {
			t.Fatalf("finite headroom must include the registered marker: %s", w.Body.String())
		}
		generation++
	}
	for _, body := range []string{`{"mode":"site_only","default_choice":"site","generation":0,"logical_limit_bytes":null}`, `{"mode":"site_only","default_choice":"site","generation":0,"default_space_capacity_bytes":null}`} {
		w := httptest.NewRecorder()
		s.SetStoragePolicy(w, credentialAPIRequest(t, u, "PUT", "/policy", body, nil))
		if w.Code != 400 {
			t.Fatalf("missing quota accepted: %d", w.Code)
		}
	}
	w := httptest.NewRecorder()
	s.SetStoragePolicy(w, credentialAPIRequest(t, u, "PUT", "/policy", `{"mode":"site_only","default_choice":"site","generation":0,"logical_limit_bytes":null,"default_space_capacity_bytes":null}`, nil))
	if w.Code != 200 {
		t.Fatalf("explicit unlimited policy: %d %s", w.Code, w.Body.String())
	}
}
func TestStorageDiskProblemMapping(t *testing.T) {
	s, _, u, _, _ := storageAPIServer(t)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{{storage.ErrDiskSpaceInsufficient, 507, "storage_disk_insufficient"}, {storage.ErrDiskSpaceUnknown, 503, "storage_disk_probe_failed"}} {
		w := httptest.NewRecorder()
		s.writeStorageError(w, credentialAPIRequest(t, u, "PUT", "/content", "", nil), tc.err)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) || w.Header().Get("Retry-After") != "" {
			t.Fatalf("disk problem: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestStorageDiskDiagnosticsAuthorizationAndUnknown(t *testing.T) {
	s, client, u, _, _ := storageAPIServer(t)
	c, err := diskspace.New(diskspace.Threshold{Bytes: 20}, func(context.Context, string) (diskspace.Observation, error) {
		return diskspace.Observation{FilesystemID: "private-volume-identity", TotalBytes: 100, AvailableBytes: 12}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s.storageSvc.SetDiskCoordinator(c, "private-host-path")
	if _, err = s.storageSvc.Diagnostics(context.Background(), u.ID, 0, 50); !errors.Is(err, service.ErrForbidden) {
		t.Fatalf("ordinary user diagnostics: %v", err)
	}
	u = client.User.UpdateOne(u).SetRole(service.SystemRoleAdmin).SaveX(context.Background())
	d, err := s.storageSvc.Diagnostics(context.Background(), u.ID, 0, 50)
	if err != nil || len(d.Disks) != 1 || d.Disks[0].State != "low" || *d.Disks[0].AvailableBytes != 12 {
		t.Fatalf("disk facts: %+v %v", d, err)
	}
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-volume-identity") || strings.Contains(string(raw), "private-host-path") {
		t.Fatal("host identity exposed")
	}
	c, err = diskspace.New(diskspace.Threshold{Bytes: 20}, func(context.Context, string) (diskspace.Observation, error) {
		return diskspace.Observation{}, errors.New("probe unavailable")
	})
	if err != nil {
		t.Fatal(err)
	}
	s.storageSvc.SetDiskCoordinator(c)
	d, err = s.storageSvc.Diagnostics(context.Background(), u.ID, 0, 50)
	if err != nil || len(d.Disks) == 0 || d.Disks[0].State != "unknown" || d.Disks[0].AvailableBytes != nil {
		t.Fatalf("unknown facts: %+v %v", d, err)
	}
}
