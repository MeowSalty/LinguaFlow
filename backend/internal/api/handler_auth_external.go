package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/authoidc"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type externalBrowserRequest struct {
	BrowserChallenge string `json:"browser_challenge"`
	CompletionURL    string `json:"completion_url"`
	ReturnTo         string `json:"return_to"`
}

func (b externalBrowserRequest) input() service.ExternalBrowserInput {
	return service.ExternalBrowserInput{BrowserChallenge: b.BrowserChallenge, CompletionURL: b.CompletionURL, ReturnTo: b.ReturnTo}
}

type externalCompleteRequest struct {
	InitialPassword   *string `json:"initial_password"`
	ConfirmationToken string  `json:"confirmation_token"`
	Verifier          string  `json:"verifier"`
	Username          string  `json:"username"`
	DisplayName       string  `json:"display_name"`
}

type externalProofRequest struct {
	ReauthenticationProof string `json:"reauthentication_proof"`
}

// New sensitive endpoints accept only the Authorization header. The historic
// query-token accommodation for EventSource is not an authentication input here.
func (s *Server) externalActor(w http.ResponseWriter, r *http.Request, required, admin bool) (*http.Request, *int, bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if s.isLocal() {
		s.writeProblem(w, r, http.StatusForbidden, "external_authentication_disabled", "本地模式不开放此认证流程")
		return r, nil, false
	}
	if s.externalAuth == nil {
		s.writeProblem(w, r, http.StatusServiceUnavailable, "authentication_unavailable", "认证服务不可用")
		return r, nil, false
	}
	if r.URL.Query().Has("access_token") {
		s.writeAuthProblem(w, r, service.ErrTokenInvalid)
		return r, nil, false
	}
	raw := bearerToken(r.Header.Get("Authorization"))
	if raw == "" {
		if required || r.Header.Get("Authorization") != "" {
			s.writeAuthProblem(w, r, errNoToken)
			return r, nil, false
		}
		return r, nil, true
	}
	account, claims, err := s.authService.ResolveUserFromAccessToken(r.Context(), raw)
	if err != nil {
		s.writeAuthProblem(w, r, err)
		return r, nil, false
	}
	if admin && account.Role != service.SystemRoleAdmin {
		s.writeServiceError(w, r, service.ErrForbidden)
		return r, nil, false
	}
	ctx := service.WithExternalActorSession(r.Context(), account.ID, claims.AuthVersion, claims.SessionID)
	return r.WithContext(ctx), &account.ID, true
}
func (s *Server) writeExternalError(w http.ResponseWriter, r *http.Request, err error) {
	var connErr *service.AuthConnectionError
	if errors.As(err, &connErr) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		issues := connErr.Issues
		if issues == nil {
			issues = []service.AuthConfigurationIssue{}
		}
		body := map[string]any{"type": "urn:linguaflow:auth-connection-" + connErr.Code, "title": "auth_connection_" + connErr.Code, "status": http.StatusConflict, "issues": issues}
		if connErr.Impact != nil {
			body["impact"] = connErr.Impact
		}
		_ = json.NewEncoder(w).Encode(body)
		return
	}
	switch {
	case errors.Is(err, service.ErrAuthenticationRuleDenied):
		s.writeProblemWithType(w, r, 403, "urn:linguaflow:authentication-rule-denied", "authentication_rule_denied", "当前渠道不满足使用条件")
	case errors.Is(err, service.ErrExternalFlowInvalid):
		s.writeProblemWithType(w, r, 400, "urn:linguaflow:external-flow-invalid", "external_flow_invalid", "认证流程无效、过期或已使用，请重新开始")
	case errors.Is(err, service.ErrReauthenticationRequired):
		s.writeProblemWithType(w, r, 403, "urn:linguaflow:reauthentication-required", "reauthentication_required", "请重新验证已有登录方式")
	case errors.Is(err, service.ErrAuthConnectionConflict), errors.Is(err, service.ErrLoginMethodConflict):
		s.writeProblemWithType(w, r, 409, "urn:linguaflow:authentication-conflict", "authentication_conflict", "配置版本、政策或可用后备方式发生冲突")
	case errors.Is(err, service.ErrAuthConnectionNotFound):
		s.writeProblem(w, r, 404, "auth_connection_not_found", "认证连接不存在或不可用")
	case errors.Is(err, service.ErrExternalUnavailable), errors.Is(err, authoidc.ErrUnavailable):
		s.writeProblemWithType(w, r, 503, "urn:linguaflow:identity-provider-unavailable", "identity_provider_unavailable", "身份服务暂时不可用，请重新开始认证")
	case errors.Is(err, authoidc.ErrInvalidConfig), errors.Is(err, authoidc.ErrInvalidToken), errors.Is(err, authoidc.ErrFreshAuth):
		s.writeProblem(w, r, 400, "external_flow_invalid", "身份认证响应不符合要求")
	default:
		s.writeServiceError(w, r, err)
	}
}
func externalStartResponse(v service.ExternalStartResult) any {
	response := map[string]any{"transaction_id": v.TransactionID, "authorization_url": v.AuthorizationURL, "expires_at": v.ExpiresAt}
	if v.TestID != "" {
		response["test_id"] = v.TestID
	}
	return response
}
func externalProofResponse(v service.ReauthenticationResult) map[string]any {
	return map[string]any{"reauthentication_proof": v.Proof, "expires_at": v.ExpiresAt}
}
func (s *Server) StartExternalAuthentication(w http.ResponseWriter, r *http.Request, id string) {
	r, _, ok := s.externalActor(w, r, false, false)
	if !ok {
		return
	}
	var in externalBrowserRequest
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	out, err := s.externalAuth.StartLogin(r.Context(), id, in.input())
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, 200, externalStartResponse(out))
}
func (s *Server) ExternalAuthenticationCallback(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if s.isLocal() || s.externalAuth == nil {
		s.writeProblem(w, r, 400, "external_flow_invalid", "认证流程不可用")
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		s.writeExternalError(w, r, service.ErrExternalFlowInvalid)
		return
	}
	for key, v := range values {
		if len(v) != 1 || !containsExternalCallbackKey(key) {
			s.writeExternalError(w, r, service.ErrExternalFlowInvalid)
			return
		}
	}
	if values.Get("state") == "" || (values.Get("code") == "") == (values.Get("error") == "") {
		s.writeExternalError(w, r, service.ErrExternalFlowInvalid)
		return
	}
	out, err := s.externalAuth.HandleCallback(r.Context(), id, values.Get("state"), values.Get("code"), values.Get("error"), values.Get("iss"))
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	target, err := url.Parse(out.CompletionURL)
	if err != nil {
		s.writeExternalError(w, r, service.ErrExternalFlowInvalid)
		return
	}
	query := target.Query()
	query.Set("ticket", out.Ticket)
	target.RawQuery = query.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}
func containsExternalCallbackKey(k string) bool {
	switch k {
	case "state", "code", "error", "error_description", "error_uri", "session_state", "iss":
		return true
	}
	return false
}
func (s *Server) ExchangeExternalAuthentication(w http.ResponseWriter, r *http.Request) {
	r, actor, ok := s.externalActor(w, r, false, false)
	if !ok {
		return
	}
	var in struct {
		Ticket   string `json:"ticket"`
		Verifier string `json:"verifier"`
	}
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	out, err := s.externalAuth.Exchange(r.Context(), actor, service.ExternalExchangeInput{Ticket: in.Ticket, Verifier: in.Verifier})
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	response := map[string]any{"kind": out.Kind}
	switch out.Kind {
	case "session":
		if out.Session == nil {
			s.writeExternalError(w, r, service.ErrExternalFlowInvalid)
			return
		}
		response["session"] = newAuthSessionResponse(out.Session)
	case "signup_confirmation", "link_confirmation":
		if out.Confirmation == nil {
			s.writeExternalError(w, r, service.ErrExternalFlowInvalid)
			return
		}
		c := out.Confirmation
		response["signup_requirements"] = c.SignupRequirements
		response["confirmation_token"] = c.ConfirmationToken
		response["transaction_id"] = c.TransactionID
		response["expires_at"] = c.ExpiresAt
		response["profile"] = map[string]any{"connection_id": c.Profile.ConnectionID, "connection_name": c.Profile.ConnectionName, "external_display": c.Profile.ExternalDisplay}
	case "reauthentication_proof":
		if out.Proof == nil {
			s.writeExternalError(w, r, service.ErrExternalFlowInvalid)
			return
		}
		for k, v := range externalProofResponse(*out.Proof) {
			response[k] = v
		}
	case "test_result":
		if out.Test == nil {
			s.writeExternalError(w, r, service.ErrExternalFlowInvalid)
			return
		}
		response["connection_id"] = out.Test.ConnectionID
		response["revision"] = out.Test.Revision
		response["fresh_supported"] = out.Test.FreshSupported
		response["rules"] = out.Test.Diagnostics
		response["attributes"] = out.Test.Attributes
		response["test_id"] = out.Test.TestID
		response["expires_at"] = out.Test.ExpiresAt
	default:
		s.writeExternalError(w, r, service.ErrExternalFlowInvalid)
		return
	}
	writeJSON(w, 200, response)
}
func (s *Server) CompleteExternalAuthentication(w http.ResponseWriter, r *http.Request) {
	r, actor, ok := s.externalActor(w, r, false, false)
	if !ok {
		return
	}
	var in externalCompleteRequest
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	out, err := s.externalAuth.Complete(r.Context(), actor, service.ExternalCompleteInput{InitialPassword: in.InitialPassword, ConfirmationToken: in.ConfirmationToken, Verifier: in.Verifier, Username: in.Username, DisplayName: in.DisplayName})
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	if out.Kind == "session" && out.Session != nil {
		writeJSON(w, 200, map[string]any{"kind": "session", "session": newAuthSessionResponse(out.Session)})
		return
	}
	if out.Kind == "linked" {
		writeJSON(w, 200, map[string]any{"kind": "linked", "binding_id": out.BindingID})
		return
	}
	s.writeExternalError(w, r, service.ErrExternalFlowInvalid)
}
func (s *Server) CancelExternalAuthentication(w http.ResponseWriter, r *http.Request) {
	r, actor, ok := s.externalActor(w, r, false, false)
	if !ok {
		return
	}
	var in struct {
		TransactionID string `json:"transaction_id"`
		Verifier      string `json:"verifier"`
	}
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	if err := s.externalAuth.Cancel(r.Context(), actor, service.ExternalCancelInput{TransactionID: in.TransactionID, Verifier: in.Verifier}); err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) ReauthenticateCurrentUser(w http.ResponseWriter, r *http.Request) {
	r, actor, ok := s.externalActor(w, r, true, false)
	if !ok {
		return
	}
	var in struct {
		externalBrowserRequest
		Method    string `json:"method"`
		Password  string `json:"password"`
		BindingID *int   `json:"binding_id"`
		Action    string `json:"action"`
		TargetID  string `json:"target_id"`
	}
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	proof, start, err := s.externalAuth.Reauthenticate(r.Context(), *actor, service.ReauthenticationInput{Method: in.Method, Password: in.Password, BindingID: in.BindingID, Action: in.Action, TargetID: in.TargetID, BrowserChallenge: in.BrowserChallenge, CompletionURL: in.CompletionURL, ReturnTo: in.ReturnTo})
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	if proof != nil {
		writeJSON(w, 200, externalProofResponse(*proof))
		return
	}
	if start != nil {
		writeJSON(w, 200, externalStartResponse(*start))
		return
	}
	s.writeExternalError(w, r, service.ErrExternalFlowInvalid)
}
func (s *Server) StartCurrentUserLoginBinding(w http.ResponseWriter, r *http.Request, id string) {
	r, actor, ok := s.externalActor(w, r, true, false)
	if !ok {
		return
	}
	var in struct {
		externalBrowserRequest
		externalProofRequest
	}
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	out, err := s.externalAuth.StartLink(r.Context(), *actor, id, service.ExternalLinkStartInput{ExternalBrowserInput: in.input(), ReauthenticationProof: in.ReauthenticationProof})
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, 200, externalStartResponse(out))
}
func (s *Server) GetCurrentUserLoginMethods(w http.ResponseWriter, r *http.Request) {
	r, actor, ok := s.externalActor(w, r, true, false)
	if !ok {
		return
	}
	out, err := s.externalAuth.LoginMethods(r.Context(), *actor)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) DeleteCurrentUserLoginBinding(w http.ResponseWriter, r *http.Request, id int) {
	r, actor, ok := s.externalActor(w, r, true, false)
	if !ok {
		return
	}
	var in externalProofRequest
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	if err := s.externalAuth.Unlink(r.Context(), *actor, id, in.ReauthenticationProof); err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) SetCurrentUserInitialPassword(w http.ResponseWriter, r *http.Request) {
	r, actor, ok := s.externalActor(w, r, true, false)
	if !ok {
		return
	}
	var in struct {
		externalProofRequest
		NewPassword string `json:"new_password"`
	}
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	if err := s.externalAuth.SetInitialPassword(r.Context(), *actor, in.NewPassword, in.ReauthenticationProof); err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) DeleteCurrentUserPassword(w http.ResponseWriter, r *http.Request) {
	r, actor, ok := s.externalActor(w, r, true, false)
	if !ok {
		return
	}
	var in externalProofRequest
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	if err := s.externalAuth.DeletePassword(r.Context(), *actor, in.ReauthenticationProof); err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) ListAuthConnections(w http.ResponseWriter, r *http.Request) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	out, err := s.externalAuth.ListConnections(r.Context(), *actor)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": out})
}
func (s *Server) GetAuthConnection(w http.ResponseWriter, r *http.Request, id string) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	out, err := s.externalAuth.GetConnection(r.Context(), *actor, id)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) CreateAuthConnection(w http.ResponseWriter, r *http.Request) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	var in service.CreateAuthConnectionInput
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	out, err := s.externalAuth.CreateConnection(r.Context(), *actor, in)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, 201, out)
}
func (s *Server) UpdateAuthConnection(w http.ResponseWriter, r *http.Request, id string) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	var in service.UpdateAuthConnectionInput
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	out, err := s.externalAuth.UpdateConnection(r.Context(), *actor, id, in)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) CheckAuthConnection(w http.ResponseWriter, r *http.Request, id string) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	var in service.AuthConnectionCandidateInput
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	out, err := s.externalAuth.CheckConnection(r.Context(), *actor, id, in)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) GetAuthConnectionImpact(w http.ResponseWriter, r *http.Request, id string) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	var in service.AuthConnectionCandidateInput
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	out, err := s.externalAuth.ImpactConnection(r.Context(), *actor, id, in)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) connectionAction(w http.ResponseWriter, r *http.Request, id string, action func(context.Context, int, string, service.AuthConnectionActionInput) (service.AuthConnectionRecord, error)) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	var in service.AuthConnectionActionInput
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	out, err := action(r.Context(), *actor, id, in)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) ConfigureAuthConnection(w http.ResponseWriter, r *http.Request, id string) {
	s.connectionAction(w, r, id, s.externalAuth.ConfigureConnection)
}
func (s *Server) EnableAuthConnection(w http.ResponseWriter, r *http.Request, id string) {
	s.connectionAction(w, r, id, s.externalAuth.EnableConnection)
}
func (s *Server) DisableAuthConnection(w http.ResponseWriter, r *http.Request, id string) {
	s.connectionAction(w, r, id, s.externalAuth.DisableConnection)
}
func (s *Server) ArchiveAuthConnection(w http.ResponseWriter, r *http.Request, id string) {
	s.connectionAction(w, r, id, s.externalAuth.ArchiveConnection)
}
func (s *Server) RestoreAuthConnection(w http.ResponseWriter, r *http.Request, id string) {
	s.connectionAction(w, r, id, s.externalAuth.RestoreConnection)
}
func (s *Server) TestAuthConnection(w http.ResponseWriter, r *http.Request, id string) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	var in service.AuthConnectionTestInput
	if !s.decodeExternalRequest(w, r, &in) {
		return
	}
	out, err := s.externalAuth.StartTest(r.Context(), *actor, id, in)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, 200, externalStartResponse(out))
}
func (s *Server) UploadAuthConnectionLogo(w http.ResponseWriter, r *http.Request, id string, p UploadAuthConnectionLogoParams) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	defer r.Body.Close()
	media := strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])
	if media != "image/png" && media != "image/webp" {
		s.writeServiceError(w, r, service.ErrInvalidInput)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		s.writeServiceError(w, r, service.ErrInvalidInput)
		return
	}
	out, err := s.externalAuth.UploadConnectionLogo(r.Context(), *actor, id, p.ExpectedRevision, data)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) GetAuthConnectionLogo(w http.ResponseWriter, r *http.Request, id string) {
	r, actor, ok := s.externalActor(w, r, true, true)
	if !ok {
		return
	}
	data, err := s.externalAuth.CurrentConnectionLogo(r.Context(), *actor, id)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeAuthenticationLogo(w, data)
}
func (s *Server) GetPublicAuthConnectionLogo(w http.ResponseWriter, r *http.Request, id string) {
	r, _, ok := s.externalActor(w, r, false, false)
	if !ok {
		return
	}
	data, err := s.externalAuth.PublicConnectionLogo(r.Context(), id)
	if err != nil {
		s.writeExternalError(w, r, err)
		return
	}
	writeAuthenticationLogo(w, data)
}
func writeAuthenticationLogo(w http.ResponseWriter, data []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(200)
	_, _ = w.Write(data)
}
