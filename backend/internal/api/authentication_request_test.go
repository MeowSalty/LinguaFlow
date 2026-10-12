package api

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestAuthenticationJSONContract(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		out        func() any
		valid      bool
	}{
		{"embedded browser", `{"browser_challenge":"challenge","completion_url":"https://app.test/complete","expected_revision":1}`, func() any {
			return &struct {
				externalBrowserRequest
				externalRevisionRequest
			}{}
		}, true},
		{"duplicate", `{"expected_revision":1,"expected_revision":2}`, func() any { return &externalRevisionRequest{} }, false},
		{"alias", `{"Expected_revision":1}`, func() any { return &externalRevisionRequest{} }, false},
		{"null scalar", `{"expected_revision":null}`, func() any { return &externalRevisionRequest{} }, false},
		{"clear secret", `{"expected_revision":1,"client_secret":null}`, func() any { return &service.UpdateAuthConnectionInput{} }, true},
		{"null create secret", `{"client_secret":null}`, func() any { return &service.CreateAuthConnectionInput{} }, false},
		{"nested alias", `{"oidc":{"Issuer":"https://id.test"}}`, func() any { return &service.CreateAuthConnectionInput{} }, false},
		{"nested duplicate", `{"oidc":{"issuer":"a","issuer":"b"}}`, func() any { return &service.CreateAuthConnectionInput{} }, false},
		{"trailing", `{"expected_revision":1} {}`, func() any { return &externalRevisionRequest{} }, false},
		{"typed condition number", `{"policy":{"login_rules":{"mode":"conditions","match":"all","conditions":[{"field_id":"custom.count","operator":"gte","value":9007199254740993}]}}}`, func() any { return &service.UpdateAuthConnectionInput{} }, true},
		{"old policy spelling", `{"policy":{"login_rule":"unrestricted"}}`, func() any { return &service.UpdateAuthConnectionInput{} }, false},
		{"sample source object", `{"expected_revision":1,"sources":{"oidc_userinfo":{"department":"engineering","nested":{"a":1},"groups":[]}}}`, func() any { return &service.AuthRulesPreviewInput{} }, true},
		{"duplicate sample source field", `{"expected_revision":1,"sources":{"oidc_userinfo":{"department":"engineering","department":"sales"}}}`, func() any { return &service.AuthRulesPreviewInput{} }, false},
		{"condition null", `{"policy":{"login_rules":{"mode":"conditions","match":"all","conditions":[{"field_id":"custom.count","operator":"gte","value":null}]}}}`, func() any { return &service.UpdateAuthConnectionInput{} }, false},
		{"condition unknown member", `{"policy":{"login_rules":{"mode":"conditions","match":"all","script":"allow"}}}`, func() any { return &service.UpdateAuthConnectionInput{} }, false},
		{"missing content type", `{"expected_revision":1}`, func() any { return &externalRevisionRequest{} }, false},
		{"wrong content type", `{"expected_revision":1}`, func() any { return &externalRevisionRequest{} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			w := httptest.NewRecorder()
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			if tc.name == "missing content type" {
				r.Header.Del("Content-Type")
			}
			if tc.name == "wrong content type" {
				r.Header.Set("Content-Type", "text/plain")
			}
			if got := s.decodeExternalRequest(w, r, tc.out()); got != tc.valid {
				t.Fatalf("valid = %v, want %v", got, tc.valid)
			}
		})
	}
}
