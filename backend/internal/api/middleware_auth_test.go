package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestLoggingNeverRecordsAuthenticationQueryValues(t *testing.T) {
	var output bytes.Buffer
	s := &Server{logger: slog.New(slog.NewJSONHandler(&output, nil))}
	handler := s.requestLoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/external/provider/callback?code=private-code&state=private-state&access_token=private-token&ticket=private-ticket&verifier=private-verifier", nil)
	r.Header.Set("Authorization", "Bearer private-header-token")
	handler.ServeHTTP(httptest.NewRecorder(), r)
	for _, value := range []string{"private-code", "private-state", "private-token", "private-ticket", "private-verifier", "private-header-token", "query"} {
		if strings.Contains(output.String(), value) {
			t.Fatalf("request log contains %q: %s", value, output.String())
		}
	}
	if !strings.Contains(output.String(), `"path":"/api/v1/auth/external/provider/callback"`) {
		t.Fatalf("request metadata missing: %s", output.String())
	}
}
