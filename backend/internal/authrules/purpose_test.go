package authrules

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func registrationRule(field, operator, value string) RuleSet {
	return RuleSet{Mode: "conditions", Match: "all", Conditions: []Condition{{FieldID: field, Operator: operator, Value: json.RawMessage(value)}}}
}

func TestCatalogPurposesAndMetadata(t *testing.T) {
	mapping := []Field{{FieldID: "custom.department", Label: "Department", Type: "string", Source: "oidc_userinfo", Pointer: "/department"}}
	oidc, err := CatalogForPurpose("oidc", mapping, purposeSignup)
	if err != nil {
		t.Fatal(err)
	}
	if len(oidc) == 0 {
		t.Fatal("empty OIDC catalog")
	}
	for _, field := range oidc {
		if len(field.AllowedOperators) == 0 {
			t.Fatalf("%s has no allowed operators", field.FieldID)
		}
	}
	local, err := CatalogForPurpose("password", nil, purposeSignup)
	if err != nil {
		t.Fatal(err)
	}
	if len(local) != 2 || local[0].FieldID != fieldRegistrationEmail || local[1].FieldID != fieldRegistrationEmailDomain {
		t.Fatalf("unexpected local catalog: %+v", local)
	}
	for _, purpose := range []string{purposeLogin, purposeLink} {
		fields, err := CatalogForPurpose("password", nil, purpose)
		if err != nil || len(fields) != 0 {
			t.Fatalf("password %s = %+v, %v", purpose, fields, err)
		}
		fields, err = CatalogForPurpose("oidc", mapping, purpose)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range fields {
			if strings.HasPrefix(field.FieldID, "registration.") {
				t.Fatalf("%s leaked registration field %s", purpose, field.FieldID)
			}
		}
	}
	if !containsField(oidc, fieldRegistrationEmailDomain) || !containsField(oidc, fieldExternalEmailDomain) {
		t.Fatal("missing signup email domains")
	}
}

func containsField(fields []Field, id string) bool {
	for _, field := range fields {
		if field.FieldID == id {
			return true
		}
	}
	return false
}

func TestPurposeValidationAndOperatorBoundaries(t *testing.T) {
	if err := ValidateForPurpose("password", nil, purposeSignup, registrationRule(fieldRegistrationEmailDomain, "contains", `"example"`)); err == nil {
		t.Fatal("domain accepted substring operator")
	}
	rule := registrationRule(fieldRegistrationEmailDomain, "in", `["Example.COM", "example.com", "xn--mnchen-3ya.de", "München.de"]`)
	normalized, err := NormalizeRuleForPurpose("password", nil, purposeSignup, rule)
	if err != nil {
		t.Fatal(err)
	}
	if string(normalized.Conditions[0].Value) != `["example.com","xn--mnchen-3ya.de"]` {
		t.Fatalf("domain normalization/dedup failed: %s", normalized.Conditions[0].Value)
	}
	for _, value := range []string{`[]`, `["example.com", "bad..example", "example.com.", "192.0.2.1", "*.example.com"]`} {
		if ValidateForPurpose("password", nil, purposeSignup, registrationRule(fieldRegistrationEmailDomain, "in", value)) == nil {
			t.Fatalf("accepted invalid domain list %s", value)
		}
	}
	for _, tc := range []struct{ operator, value string }{
		{"contains", `""`}, {"not_contains", `""`}, {"starts_with", `""`}, {"not_starts_with", `""`}, {"ends_with", `""`}, {"not_ends_with", `""`},
	} {
		if ValidateForPurpose("password", nil, purposeSignup, registrationRule(fieldRegistrationEmail, tc.operator, tc.value)) == nil {
			t.Fatalf("accepted empty %s", tc.operator)
		}
	}
	for _, tc := range []struct{ operator, value string }{
		{"contains", `"bridge"`}, {"not_contains", `"blocked"`}, {"starts_with", `"staging-"`}, {"not_starts_with", `"prod-"`}, {"ends_with", `"-beta"`}, {"not_ends_with", `"-prod"`},
	} {
		if err := ValidateForPurpose("oidc", []Field{{FieldID: "custom.label", Label: "Label", Type: "string", Source: "oidc_id_token", Pointer: "/label"}}, purposeLogin, registrationRule("custom.label", tc.operator, tc.value)); err != nil {
			t.Fatalf("%s %s: %v", tc.operator, tc.value, err)
		}
	}
}

func TestDomainExtractionAndUnknownNegation(t *testing.T) {
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	rule := registrationRule(fieldRegistrationEmailDomain, "not_in", `["example.com"]`)
	result := EvaluateForPurpose("password", purposeSignup, rule, nil, Evidence{}, now)
	if result.Status != "unknown" {
		t.Fatalf("missing domain negation: %+v", result)
	}
	source := json.RawMessage(`{"email":"User@München.DE"}`)
	attrs, err := ExtractForPurpose("password", nil, purposeSignup, map[string]json.RawMessage{"registration": source}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if string(attrs[fieldRegistrationEmailDomain].Value) != `"xn--mnchen-3ya.de"` || attrs[fieldRegistrationEmailDomain].Status != "pass" {
		t.Fatalf("normalized domain extraction: %+v", attrs[fieldRegistrationEmailDomain])
	}
	result = EvaluateForPurpose("password", purposeSignup, rule, nil, Evidence{ObservedAt: now, Attributes: attrs}, now)
	if result.Status != "pass" {
		t.Fatalf("normalized not_in evaluation: %+v", result)
	}
	invalid := json.RawMessage(`{"email":"user@192.0.2.1"}`)
	attrs, err = ExtractForPurpose("password", nil, purposeSignup, map[string]json.RawMessage{"registration": invalid}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if attrs[fieldRegistrationEmailDomain].Status != "unknown" || attrs[fieldRegistrationEmailDomain].Reason != "invalid_type" {
		t.Fatalf("IP domain was accepted: %+v", attrs[fieldRegistrationEmailDomain])
	}
}

func TestStringNegationUnknownUsualCaseAndCaseSensitivity(t *testing.T) {
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ field, operator, value string }{
		{fieldRegistrationEmail, "ne", `"blocked@example.com"`},
		{fieldRegistrationEmail, "not_in", `["blocked@example.com","other@example.com"]`},
		{"custom.label", "not_contains", `"blocked"`},
		{"custom.label", "not_starts_with", `"blocked-"`},
		{"custom.label", "not_ends_with", `"-blocked"`},
		{"custom.labels", "contains", `"blocked"`},
	} {
		kind := "string"
		if tc.field == "custom.labels" {
			kind = "string_list"
		}
		fields := []Field{{FieldID: tc.field, Label: "Test", Type: kind, Source: "oidc_id_token", Pointer: "/value"}}
		got := EvaluateForPurpose("oidc", purposeLogin, registrationRule(tc.field, tc.operator, tc.value), fields, Evidence{}, now)
		if got.Status != "unknown" {
			t.Fatalf("%s %s missing value = %+v", tc.operator, tc.field, got)
		}
	}
	fields := []Field{{FieldID: "custom.label", Label: "Label", Type: "string", Source: "oidc_id_token", Pointer: "/label"}}
	exact := `["Ünïcode", "EN"]`
	attrs, err := ExtractForPurpose("oidc", fields, purposeLogin, map[string]json.RawMessage{"oidc_id_token": json.RawMessage(`{"label":"Ünïcode"}`)}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if EvaluateForPurpose("oidc", purposeLogin, registrationRule("custom.label", "in", exact), fields, Evidence{ObservedAt: now, Attributes: attrs}, now).Status != "pass" {
		t.Fatal("in was not exact")
	}
	if EvaluateForPurpose("oidc", purposeLogin, registrationRule("custom.label", "in", `["ünïcode", "en"]`), fields, Evidence{ObservedAt: now, Attributes: attrs}, now).Status != "fail" {
		t.Fatal("string list compared case-insensitively")
	}
}

func TestEmailSourcePairingEvaluation(t *testing.T) {
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	fields, err := CatalogForPurpose("oidc", nil, purposeSignup)
	if err != nil {
		t.Fatal(err)
	}
	catalog := fieldMap(fields)
	rule := registrationRule(fieldExternalEmailVerified, "eq", `true`)
	sameSource, err := ExtractForPurpose("oidc", nil, purposeSignup, map[string]json.RawMessage{
		"oidc_id_token": json.RawMessage(`{"email":"alice@example.com","email_verified":true}`),
	}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := EvaluateForPurpose("oidc", purposeSignup, rule, nil, Evidence{ObservedAt: now, Attributes: sameSource}, now); got.Status != "pass" {
		t.Fatalf("same-source pairing: %+v", got)
	}
	if got := EvaluateForPurpose("oidc", purposeSignup, registrationRule(fieldExternalEmailDomain, "eq", `"example.com"`), nil, Evidence{ObservedAt: now, Attributes: sameSource}, now); got.Status != "pass" {
		t.Fatalf("paired domain: %+v", got)
	}
	idOnlyVerified, err := ExtractForPurpose("oidc", nil, purposeSignup, map[string]json.RawMessage{
		"oidc_id_token": json.RawMessage(`{"email_verified":true}`),
		"oidc_userinfo": json.RawMessage(`{"email":"userinfo@example.com"}`),
	}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := EvaluateForPurpose("oidc", purposeSignup, rule, nil, Evidence{ObservedAt: now, Attributes: idOnlyVerified}, now); got.Status == "pass" {
		t.Fatalf("UserInfo email paired with ID-token verified: %+v", got)
	}
	userInfoVerified := catalog[fieldExternalEmailVerified]
	userInfoVerified.Source = "oidc_userinfo"
	crossPair := map[string]Attribute{
		fieldExternalEmail:         {Status: "pass", Value: json.RawMessage(`"alice@example.com"`), Fingerprint: FingerprintField(catalog[fieldExternalEmail])},
		fieldExternalEmailVerified: {Status: "pass", Value: json.RawMessage(`true`), Fingerprint: FingerprintField(userInfoVerified)},
	}
	got := EvaluateForPurpose("oidc", purposeSignup, rule, nil, Evidence{ObservedAt: now, Attributes: crossPair}, now)
	if got.Status == "pass" || len(got.Conditions) == 0 || got.Conditions[0].Reason != "mapping_changed" {
		t.Fatalf("cross-source verified was paired: %+v", got)
	}
	conflicting, err := ExtractForPurpose("oidc", nil, purposeSignup, map[string]json.RawMessage{
		"oidc_id_token": json.RawMessage(`{"email":"alice@example.com","email_verified":true}`),
		"oidc_userinfo": json.RawMessage(`{"email":"mallory@other.example"}`),
	}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if string(conflicting[fieldExternalEmail].Value) != `"alice@example.com"` || conflicting[fieldExternalEmailDomain].Value == nil {
		t.Fatalf("UserInfo email leaked across pair: %+v", conflicting[fieldExternalEmail])
	}
	if conflicting[fieldExternalEmail].Value == nil && conflicting[fieldExternalEmailDomain].Value == nil {
		t.Fatal("missing paired email should be unknown")
	}
}

func TestDomainListNormalizationDedupBoundary(t *testing.T) {
	tenantValues := make([]string, 0, 150)
	for i := range 100 {
		tenantValues = append(tenantValues, fmt.Sprintf("Example-%02d.COM", i))
	}
	for i := range 50 {
		tenantValues = append(tenantValues, fmt.Sprintf("example-%02d.com", i))
	}
	encoded, _ := json.Marshal(tenantValues)
	if err := ValidateForPurpose("password", nil, purposeSignup, registrationRule(fieldRegistrationEmailDomain, "in", string(encoded))); err != nil {
		t.Fatalf("150 raw values reduced to 100 distinct domains rejected: %v", err)
	}
	values := make([]string, 0, 100)
	for i := range 100 {
		values = append(values, fmt.Sprintf("EXAMPLE-%02d.COM", i))
	}
	encoded100, _ := json.Marshal(values)
	if err := ValidateForPurpose("password", nil, purposeSignup, registrationRule(fieldRegistrationEmailDomain, "in", string(encoded100))); err != nil {
		t.Fatalf("100 domains rejected: %v", err)
	}
	values = append(values, "Example.COM")
	encoded, _ = json.Marshal(values)
	if err := ValidateForPurpose("password", nil, purposeSignup, registrationRule(fieldRegistrationEmailDomain, "in", string(encoded))); err == nil {
		t.Fatal("accepted more than 100 domains")
	}
	values = values[:100]
	values[0], values[1] = "Example.COM", "example.com"
	encoded, _ = json.Marshal(values)
	normalized, err := NormalizeRuleForPurpose("password", nil, purposeSignup, registrationRule(fieldRegistrationEmailDomain, "in", string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	var observed []string
	if err := json.Unmarshal(normalized.Conditions[0].Value, &observed); err != nil {
		t.Fatal(err)
	}
	hasCanonical := false
	for _, value := range observed {
		if value == "example.com" {
			hasCanonical = true
			break
		}
	}
	if len(observed) != 99 || !hasCanonical {
		t.Fatalf("duplicate domain retained: %+v", observed)
	}
}

func TestNormalizationCopiesAndDirectDomainEvaluation(t *testing.T) {
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	rule := registrationRule(fieldRegistrationEmailDomain, "in", `["Example.COM","example.com","München.DE"]`)
	before, _ := json.Marshal(rule)
	if _, err := NormalizeRuleForPurpose("password", nil, purposeSignup, rule); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(rule)
	if string(before) != string(after) {
		t.Fatalf("normalization mutated caller rule: before=%s after=%s", before, after)
	}
	source := json.RawMessage(`{"email":"USER@Example.COM"}`)
	attrs, err := ExtractForPurpose("password", nil, purposeSignup, map[string]json.RawMessage{"registration": source}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if EvaluateForPurpose("password", purposeSignup, registrationRule(fieldRegistrationEmailDomain, "eq", `"Example.COM"`), nil, Evidence{ObservedAt: now, Attributes: attrs}, now).Status != "pass" {
		t.Fatal("direct evaluation did not normalize condition domain")
	}
	if EvaluateForPurpose("password", purposeSignup, registrationRule(fieldRegistrationEmailDomain, "in", `["MÜNCHEN.DE"]`), nil, Evidence{ObservedAt: now, Attributes: attrs}, now).Status != "fail" {
		t.Fatal("direct in evaluation did not use normalized comparison")
	}
}

func TestOIDCPairingNeverConcatenatesConflictingAddresses(t *testing.T) {
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	fields, err := CatalogForPurpose("oidc", nil, purposeSignup)
	if err != nil {
		t.Fatal(err)
	}
	catalog := fieldMap(fields)
	rule := registrationRule(fieldExternalEmailVerified, "eq", `true`)
	idEmail := `{"email":"id@example.com"}`
	idVerified := `{"email_verified":true}`
	userEmail := `{"email":"userinfo@other.example"}`
	userVerified := `{"email_verified":true}`
	cases := []struct {
		name     string
		idToken  string
		userInfo string
		verified string
	}{
		{"same", `{"email":"id@example.com","email_verified":true}`, `{}`, "pass"},
		{"id-email-userinfo-verified", idEmail, userVerified, "unknown"},
		{"user-email-id-verified", idVerified, userEmail, "unknown"},
	}
	for _, tc := range cases {
		attrs, err := ExtractForPurpose("oidc", nil, purposeSignup, map[string]json.RawMessage{
			"oidc_id_token": json.RawMessage(tc.idToken),
			"oidc_userinfo": json.RawMessage(tc.userInfo),
		}, nil, now)
		if err != nil {
			t.Fatal(err)
		}
		got := EvaluateForPurpose("oidc", purposeSignup, rule, nil, Evidence{ObservedAt: now, Attributes: attrs}, now)
		if got.Status != tc.verified {
			t.Fatalf("%s: got %+v want %s", tc.name, got, tc.verified)
		}
		if got.Status == "pass" && string(attrs[fieldExternalEmail].Value) != `"id@example.com"` {
			t.Fatalf("%s tipped address into trust: %s", tc.name, attrs[fieldExternalEmail].Value)
		}
	}
	conflict, err := ExtractForPurpose("oidc", nil, purposeSignup, map[string]json.RawMessage{
		"oidc_id_token": json.RawMessage(`{"email":"id@example.com","email_verified":true}`),
		"oidc_userinfo": json.RawMessage(userEmail),
	}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if string(conflict[fieldExternalEmail].Value) != `"id@example.com"` || conflict[fieldExternalEmailDomain].Value == nil {
		t.Fatalf("conflicting UserInfo address entered trusted pair: %+v", conflict[fieldExternalEmail])
	}
	if _, ok := catalog[fieldExternalEmailVerified]; !ok || catalog[fieldExternalEmailVerified].Source != catalog[fieldExternalEmail].Source {
		t.Fatal("OIDC catalog did not bind verified field to email source")
	}
}

func TestSourcePairingAndFingerprintDependencies(t *testing.T) {
	mapping := []Field{{FieldID: "custom.department", Label: "Department", Type: "string", Source: "oidc_userinfo", Pointer: "/department"}}
	fields, err := CatalogForPurpose("oidc", mapping, purposeSignup)
	if err != nil {
		t.Fatal(err)
	}
	catalog := fieldMap(fields)
	base := catalog[fieldExternalEmail]
	if catalog[fieldExternalEmailVerified].TrustPair == nil || catalog[fieldExternalEmailVerified].TrustPair.PeerFieldID != fieldExternalEmail || catalog[fieldExternalEmailVerified].TrustPair.PeerSource != base.Source {
		t.Fatalf("broken email verified pairing: %+v", catalog[fieldExternalEmailVerified].TrustPair)
	}
	if FingerprintField(base) == FingerprintField(catalog[fieldExternalEmailDomain]) {
		t.Fatal("derived domain shares base fingerprint")
	}
	pointerChanged := base
	pointerChanged.Pointer = "/address"
	if FingerprintField(base) == FingerprintField(pointerChanged) {
		t.Fatal("source pointer did not invalidate derived fingerprint")
	}
	baseDependency := Field{FieldID: fieldExternalEmail, Label: "Email", Type: "string", Source: "oauth2_profile", Pointer: "/email", SemanticFormat: semanticEmail, NormalizationVersion: "plain_address/v1"}
	derivedDependency := Field{FieldID: fieldExternalEmailDomain, Label: "Domain", Type: "string", Source: "derived", Pointer: fieldExternalEmail, DerivedFrom: []string{fieldExternalEmail}, SemanticFormat: semanticEmailDomain}
	dependencyFields := []Field{baseDependency, derivedDependency}
	if err := setDependencyFingerprints(dependencyFields); err != nil {
		t.Fatal(err)
	}
	beforeDependency := fieldMap(dependencyFields)[fieldExternalEmailDomain].DependencyFingerprint
	baseDependency.NormalizationVersion = "plain_address/v2"
	dependencyFields = []Field{baseDependency, derivedDependency}
	if err := setDependencyFingerprints(dependencyFields); err != nil {
		t.Fatal(err)
	}
	if beforeDependency == fieldMap(dependencyFields)[fieldExternalEmailDomain].DependencyFingerprint {
		t.Fatal("base normalization version did not invalidate dependency fingerprint")
	}
	rule := registrationRule(fieldExternalEmailDomain, "eq", `"example.com"`)
	beforeRule := RulesFingerprintForPurpose("oidc", purposeSignup, rule, mapping)
	mapping[0].Source = "oauth2_profile"
	mapping = []Field{{FieldID: fieldExternalEmail, Label: "External email", Type: "boolean", Source: "oauth2_profile", Pointer: "/email"}}
	if _, err := Catalog("oauth2", mapping); err == nil {
		t.Fatal("ordinary OAuth trusted a non-string email mapping")
	}
	mapping[0].Type = "string"
	ordinary, err := CatalogForPurpose("oauth2", mapping, purposeSignup)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range ordinary {
		if field.FieldID == fieldExternalEmailVerified {
			t.Fatal("ordinary OAuth exposed trusted email_verified")
		}
	}
	if beforeRule == "" {
		t.Fatal("fingerprint helper unavailable")
	}
}

func TestRegistryCycleDetectionAndFieldIsolation(t *testing.T) {
	if err := validateDerivedRegistry(derivedDefinitions); err != nil {
		t.Fatal(err)
	}
	cyclic := []derivedDefinition{
		{FieldID: "external.a", DependsOn: []string{"external.b"}, Applicable: func(string, string, map[string]Field) bool { return true }, Build: func(map[string]Field, string) Field { return Field{} }},
		{FieldID: "external.b", DependsOn: []string{"external.a"}, Applicable: func(string, string, map[string]Field) bool { return true }, Build: func(map[string]Field, string) Field { return Field{} }},
	}
	if err := validateDerivedRegistry(cyclic); err == nil {
		t.Fatal("cycle accepted")
	}
	_, err := CatalogForPurpose("oauth2", nil, purposeLogin)
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := ExtractForPurpose("oauth2", nil, purposeLogin, nil, nil, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	for id := range attrs {
		if strings.HasPrefix(id, "registration.") {
			t.Fatalf("registration attribute leaked to login: %s", id)
		}
	}
}

func TestRequiredAttributesWalkDerivedAndTrustDependencies(t *testing.T) {
	fields, err := CatalogForPurpose("oidc", nil, purposeLogin)
	if err != nil {
		t.Fatal(err)
	}
	catalog := fieldMap(fields)
	ids := RequiredAttributesForPurpose("oidc", nil, purposeLogin, registrationRule(fieldExternalEmailVerified, "eq", `true`))
	if len(ids) != 2 || ids[0] != fieldExternalEmail || ids[1] != fieldExternalEmailVerified {
		t.Fatalf("verified dependency dropped email: %v", ids)
	}
	ids = RequiredAttributesForPurpose("password", nil, purposeSignup, registrationRule(fieldRegistrationEmailDomain, "eq", `"example.com"`))
	if len(ids) != 2 || ids[0] != fieldRegistrationEmail || ids[1] != fieldRegistrationEmailDomain {
		t.Fatalf("derived dependency walked incorrectly: %v", ids)
	}
	old := RequiredAttributes(nil, registrationRule(fieldExternalEmailVerified, "eq", `true`))
	if len(old) != 2 || old[0] != fieldExternalEmail || old[1] != fieldExternalEmailVerified {
		t.Fatalf("old wrapper dependency differs: %v", old)
	}
	if err := ValidateScopesForPurpose("oidc", nil, []string{"email"}, false, purposeLogin, registrationRule(fieldExternalEmailVerified, "eq", `true`)); err != nil {
		t.Fatalf("same-token pair rejected: %v", err)
	}
	if err := ValidateScopesForPurpose("oidc", nil, nil, true, purposeLogin, registrationRule(fieldExternalEmailVerified, "eq", `true`)); err == nil {
		t.Fatal("missing required scope accepted")
	}
	changed := fieldMap(fields)
	base := changed[fieldExternalEmail]
	base.Pointer = "/other_email"
	pair := []Field{base, changed[fieldExternalEmailVerified]}
	for i := range pair {
		pair[i].DependencyFingerprint = ""
	}
	if err := setDependencyFingerprints(pair); err != nil {
		t.Fatal(err)
	}
	originalPair := append([]Field{catalog[fieldExternalEmail]}, catalog[fieldExternalEmailVerified])
	for i := range originalPair {
		originalPair[i].DependencyFingerprint = ""
	}
	if err := setDependencyFingerprints(originalPair); err != nil {
		t.Fatal(err)
	}
	if fieldMap(originalPair)[fieldExternalEmailVerified].DependencyFingerprint == fieldMap(pair)[fieldExternalEmailVerified].DependencyFingerprint {
		t.Fatal("verified fingerprint ignored peer mapping")
	}
	trustCycle := []derivedDefinition{
		{FieldID: "external.a", TrustDeps: []string{"external.b"}},
		{FieldID: "external.b", TrustDeps: []string{"external.a"}},
	}
	if err := validateDerivedRegistry(trustCycle); err == nil {
		t.Fatal("trust-pair cycle accepted")
	}
}

func TestB5DomainMatchesOnlyCanonicalExactName(t *testing.T) {
	now := time.Now().UTC()
	for _, purpose := range []struct{ protocol, purpose, source, field string }{
		{"password", "signup", "registration", fieldRegistrationEmailDomain},
		{"oidc", "login", "oidc_id_token", fieldExternalEmailDomain},
	} {
		for _, tc := range []struct{ address, allowed, status string }{
			{"user@Example.COM", "example.com", "pass"},
			{"user@sub.example.com", "example.com", "fail"},
			{"user@evil-example.com", "example.com", "fail"},
			{"user@example.com.evil.org", "example.com", "fail"},
			{"user@München.DE", "xn--mnchen-3ya.de", "pass"},
			{"user@xn--mnchen-3ya.de", "München.de", "pass"},
		} {
			source, _ := json.Marshal(map[string]string{"email": tc.address})
			attrs, err := ExtractForPurpose(purpose.protocol, nil, purpose.purpose, map[string]json.RawMessage{purpose.source: source}, nil, now)
			if err != nil {
				t.Fatal(err)
			}
			value, _ := json.Marshal(tc.allowed)
			rule := registrationRule(purpose.field, "eq", string(value))
			result := EvaluateForPurpose(purpose.protocol, purpose.purpose, rule, nil, Evidence{ObservedAt: now, Attributes: attrs}, now)
			if result.Status != tc.status {
				t.Fatalf("%s address=%q allowed=%q: %+v, want %s", purpose.field, tc.address, tc.allowed, result, tc.status)
			}
		}
	}
}

func TestB5TextOperatorsAndListElements(t *testing.T) {
	now := time.Now().UTC()
	fields := []Field{
		{FieldID: "custom.text", Label: "Text", Type: "string", Source: "oidc_id_token", Pointer: "/text"},
		{FieldID: "custom.tags", Label: "Tags", Type: "string_list", Source: "oidc_id_token", Pointer: "/tags"},
	}
	attrs, err := ExtractForPurpose("oidc", fields, "login", map[string]json.RawMessage{"oidc_id_token": json.RawMessage(`{"text":"研发Ü-beta","tags":["engineering","ops"]}`)}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ field, operator, value, status string }{
		{"custom.text", "contains", `"Ü"`, "pass"},
		{"custom.text", "contains", `"ü"`, "fail"},
		{"custom.text", "not_contains", `"ü"`, "pass"},
		{"custom.text", "starts_with", `"研发Ü"`, "pass"},
		{"custom.text", "not_starts_with", `"研发Ü"`, "fail"},
		{"custom.text", "ends_with", `"-beta"`, "pass"},
		{"custom.text", "not_ends_with", `"-beta"`, "fail"},
		{"custom.tags", "contains", `"engine"`, "fail"},
		{"custom.tags", "not_contains", `"engine"`, "pass"},
		{"custom.tags", "contains", `"engineering"`, "pass"},
	} {
		result := EvaluateForPurpose("oidc", "login", registrationRule(tc.field, tc.operator, tc.value), fields, Evidence{ObservedAt: now, Attributes: attrs}, now)
		if result.Status != tc.status {
			t.Fatalf("%s %s %s: %+v, want %s", tc.field, tc.operator, tc.value, result, tc.status)
		}
	}
}

func TestB5TextSetDistinctItemBoundary(t *testing.T) {
	for _, tc := range []struct{ kind, operator string }{
		{"string", "in"}, {"string", "not_in"},
		{"string_list", "contains_any"}, {"string_list", "contains_all"},
	} {
		fields := []Field{{FieldID: "custom.values", Label: "Values", Type: tc.kind, Source: "oidc_id_token", Pointer: "/values"}}
		values := make([]string, 1024)
		for i := range values {
			values[i] = fmt.Sprintf("value-%04d", i)
		}
		values = append(values, values[0])
		encoded, _ := json.Marshal(values)
		if err := ValidateForPurpose("oidc", fields, "login", registrationRule("custom.values", tc.operator, string(encoded))); err != nil {
			t.Fatalf("%s rejected 1024 distinct values after deduplication: %v", tc.operator, err)
		}
		values = append(values, "extra-distinct-value")
		encoded, _ = json.Marshal(values)
		if err := ValidateForPurpose("oidc", fields, "login", registrationRule("custom.values", tc.operator, string(encoded))); err == nil {
			t.Fatalf("%s accepted 1025 distinct values", tc.operator)
		}
	}
}
