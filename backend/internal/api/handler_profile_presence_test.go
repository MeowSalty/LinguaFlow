package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

func TestProfilePresenceSurvivesAPIAndDatabaseRoundTrip(t *testing.T) {
	s, _, _ := configurationConsumerServer(t, config.ModeLocal, nil)
	router := s.newRouter()
	request := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	w := request(http.MethodPost, "/execution-profiles", `{"name":"presence","config":{"context":{"enabled":false,"before":0,"after":0},"ruby":{"preserve_kinds":[]},"qa":{"checks":[]}}}`, http.StatusCreated)
	var created struct {
		ID     int             `json:"id"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	profile, err := execution.DecodeProfileJSON(created.Config, execution.DefaultProfile())
	if err != nil {
		t.Fatalf("API config cannot be read back: %v", err)
	}
	if profile.Context.Enabled || profile.Context.Before != 0 || profile.Context.After != 0 || profile.Ruby.PreserveKinds == nil || len(profile.Ruby.PreserveKinds) != 0 || profile.QA.Checks == nil || len(profile.QA.Checks) != 0 {
		t.Fatal("explicit false/zero/empty values changed")
	}
	path := fmt.Sprintf("/execution-profiles/%d", created.ID)
	request(http.MethodPut, path, `{"config":`+string(created.Config)+`}`, http.StatusOK)
	updated := request(http.MethodGet, path, "", http.StatusOK)
	var read struct {
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	got, err := execution.DecodeProfileJSON(read.Config, execution.DefaultProfile())
	if err != nil || !reflect.DeepEqual(profile, got) {
		t.Fatalf("profile round trip changed: %v", err)
	}
}
