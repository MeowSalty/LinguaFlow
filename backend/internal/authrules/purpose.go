package authrules

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/accountidentity"
)

const (
	purposeSignup = "signup"
	purposeLogin  = "login"
	purposeLink   = "link"

	fieldRegistrationEmail       = "registration.email"
	fieldRegistrationEmailDomain = "registration.email_domain"
	fieldExternalEmail           = "external.email"
	fieldExternalEmailVerified   = "external.email_verified"
	fieldExternalEmailDomain     = "external.email_domain"

	semanticEmail       = "email"
	semanticEmailDomain = "email_domain"
)

var (
	stringOperators = []string{"eq", "ne", "in", "not_in", "contains", "not_contains", "starts_with", "not_starts_with", "ends_with", "not_ends_with"}
	domainOperators = []string{"eq", "ne", "in", "not_in"}
)

func normalizePurpose(purpose string) (string, error) {
	switch purpose {
	case purposeSignup, purposeLogin, purposeLink:
		return purpose, nil
	}
	return "", fmt.Errorf("unsupported rules purpose")
}

func stringValueOperators() []string { return append([]string{}, stringOperators...) }
func domainValueOperators() []string { return append([]string{}, domainOperators...) }
func normalizationVersion() string   { return accountidentity.DomainNormalizationVersion() }

func fillMetadata(f Field) Field {
	switch f.Type {
	case "string":
		f.AllowedOperators = stringValueOperators()
	case "boolean":
		f.AllowedOperators = []string{"eq"}
	case "integer", "number":
		f.AllowedOperators = []string{"eq", "ne", "gt", "gte", "lt", "lte"}
	case "datetime":
		f.AllowedOperators = []string{"gt", "gte", "lt", "lte"}
	case "string_list":
		f.AllowedOperators = []string{"contains", "not_contains", "contains_any", "contains_all"}
	}
	if f.SemanticFormat == semanticEmailDomain {
		f.AllowedOperators = domainValueOperators()
		f.NormalizationVersion = normalizationVersion()
		if f.TrustPair == nil && f.DerivedFrom != nil {
			f.TrustPair = &FieldTrustPair{PeerFieldID: f.DerivedFrom[0]}
		}
	}
	return f
}

func CatalogForPurpose(protocol string, mappings []Field, purpose string) ([]Field, error) {
	p, err := normalizePurpose(purpose)
	if err != nil {
		return nil, err
	}
	base, err := baseCatalog(protocol, mappings)
	if err != nil {
		return nil, err
	}
	fields := []Field{}
	if p == purposeSignup {
		fields = append(fields, Field{FieldID: fieldRegistrationEmail, Label: "Registration email", Type: "string", Source: "registration", Pointer: "/email", Optional: true, UserEditable: true, SemanticFormat: semanticEmail, NormalizationVersion: "plain_address/v1"})
	}
	for _, f := range base {
		if strings.HasPrefix(f.FieldID, "registration.") {
			continue
		}
		fields = append(fields, fillMetadata(f))
	}
	email, emailFound := fieldMap(base)[fieldExternalEmail]
	verified, verifiedFound := fieldMap(base)[fieldExternalEmailVerified]
	if emailFound {
		email.Source = strings.TrimSpace(email.Source)
		email.Pointer = strings.TrimSpace(email.Pointer)
		email.SemanticFormat = semanticEmail
		email.NormalizationVersion = "plain_address/v1"
		email.UserEditable = true
		setField(fields, email)
	}
	if verifiedFound {
		verified.TrustPair = &FieldTrustPair{PeerFieldID: fieldExternalEmail, PeerSource: email.Source}
		setField(fields, fillMetadata(verified))
	}
	fields, err = applyDerivedFields(protocol, p, fields)
	if err != nil {
		return nil, err
	}
	return sortFieldsByID(fields), nil
}

type derivedDefinition struct {
	FieldID    string
	DependsOn  []string
	TrustDeps  []string
	Applicable func(protocol, purpose string, catalog map[string]Field) bool
	Build      func(catalog map[string]Field, version string) Field
	Extract    func(f Field, catalog map[string]Field, attrs map[string]Attribute, sourceErrors map[string]string, now time.Time) Attribute
	Evidence   func(f Field, catalog map[string]Field, evidence Evidence, now time.Time) (json.RawMessage, error)
}

var derivedDefinitions = []derivedDefinition{
	{
		FieldID: "external.account_age_days", DependsOn: []string{"external.account_created_at"},
		Applicable: func(_, _ string, catalog map[string]Field) bool {
			_, ok := catalog["external.account_created_at"]
			return ok
		},
		Build: func(catalog map[string]Field, _ string) Field {
			return Field{FieldID: "external.account_age_days", Label: "Account age (days)", Type: "integer", Source: "derived", Pointer: "external.account_created_at", Optional: true, UserEditable: false, DerivedFrom: []string{"external.account_created_at"}}
		},
		Extract:  extractAccountAge,
		Evidence: evidenceAccountAge,
	},
	{
		FieldID: fieldRegistrationEmailDomain, DependsOn: []string{fieldRegistrationEmail},
		TrustDeps: []string{fieldRegistrationEmail},
		Applicable: func(_, purpose string, catalog map[string]Field) bool {
			_, ok := catalog[fieldRegistrationEmail]
			return purpose == purposeSignup && ok
		},
		Build: func(catalog map[string]Field, version string) Field {
			return Field{FieldID: fieldRegistrationEmailDomain, Label: "Registration email domain", Type: "string", Source: "derived", Pointer: fieldRegistrationEmail, Optional: true, UserEditable: false, DerivedFrom: []string{fieldRegistrationEmail}, SemanticFormat: semanticEmailDomain, NormalizationVersion: version}
		},
		Extract:  extractEmailDomain,
		Evidence: evidenceEmailDomain,
	},
	{
		FieldID: fieldExternalEmailDomain, DependsOn: []string{fieldExternalEmail},
		TrustDeps: []string{fieldExternalEmail},
		Applicable: func(_, _ string, catalog map[string]Field) bool {
			_, ok := catalog[fieldExternalEmail]
			return ok
		},
		Build: func(catalog map[string]Field, version string) Field {
			return Field{FieldID: fieldExternalEmailDomain, Label: "External email domain", Type: "string", Source: "derived", Pointer: fieldExternalEmail, Optional: true, UserEditable: false, DerivedFrom: []string{fieldExternalEmail}, SemanticFormat: semanticEmailDomain, NormalizationVersion: version}
		},
		Extract:  extractEmailDomain,
		Evidence: evidenceEmailDomain,
	},
}

func extractEmailDomain(f Field, catalog map[string]Field, attrs map[string]Attribute, sourceErrors map[string]string, _ time.Time) Attribute {
	a := Attribute{Status: "unknown", Reason: "missing", Fingerprint: FingerprintField(f)}
	if len(f.DerivedFrom) != 1 {
		a.Reason = "invalid_dependency"
		return a
	}
	dependency := catalog[f.DerivedFrom[0]]
	base, ok := attrs[f.DerivedFrom[0]]
	if !ok {
		return a
	}
	if reason := sourceErrors[dependency.Source]; reason != "" {
		a.Reason = reason
		return a
	}
	if base.Fingerprint != FingerprintField(dependency) || base.Status != "pass" {
		a.Reason = "missing_dependency"
		return a
	}
	var address string
	if json.Unmarshal(base.Value, &address) != nil {
		a.Reason = "invalid_type"
		return a
	}
	domain, err := accountidentity.NormalizeEmailDomain(address)
	if err != nil {
		a.Reason = "invalid_type"
		return a
	}
	value, _ := json.Marshal(domain)
	a.Value, a.Status, a.Reason = value, "pass", ""
	return a
}

func extractAccountAge(f Field, catalog map[string]Field, attrs map[string]Attribute, _ map[string]string, now time.Time) Attribute {
	a := Attribute{Status: "unknown", Reason: "missing", Fingerprint: FingerprintField(f)}
	base, ok := attrs["external.account_created_at"]
	if !ok {
		return a
	}
	a.Status, a.Reason = base.Status, base.Reason
	if base.Status != "pass" {
		return a
	}
	if base.Fingerprint != FingerprintField(catalog["external.account_created_at"]) {
		a.Status, a.Reason = "unknown", "mapping_changed"
		return a
	}
	var value string
	if json.Unmarshal(base.Value, &value) != nil {
		a.Status, a.Reason = "unknown", "invalid_type"
		return a
	}
	when, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || when.After(now) {
		a.Status, a.Reason = "unknown", "invalid_type"
		return a
	}
	encoded := json.RawMessage(strconv.FormatInt(accountAgeDays(now, when), 10))
	a.Value, a.Status, a.Reason = encoded, "pass", ""
	return a
}

func evidenceAccountAge(f Field, catalog map[string]Field, evidence Evidence, now time.Time) (json.RawMessage, error) {
	attribute := extractAccountAge(f, catalog, evidence.Attributes, nil, now)
	if attribute.Status != "pass" {
		return nil, errors.New(attribute.Reason)
	}
	return attribute.Value, nil
}

func evidenceEmailDomain(f Field, catalog map[string]Field, evidence Evidence, _ time.Time) (json.RawMessage, error) {
	attribute := extractEmailDomain(f, catalog, evidence.Attributes, nil, time.Time{})
	if attribute.Status != "pass" {
		return nil, errors.New(attribute.Reason)
	}
	return attribute.Value, nil
}

func validateDerivedRegistry(defs []derivedDefinition) error {
	seen := map[string]bool{}
	for _, definition := range defs {
		if definition.FieldID == "" || seen[definition.FieldID] {
			return fmt.Errorf("invalid derived registry")
		}
		seen[definition.FieldID] = true
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visited[id] {
			return nil
		}
		if visiting[id] {
			return fmt.Errorf("derived dependency cycle at %s", id)
		}
		visiting[id] = true
		index := -1
		for i := range defs {
			if defs[i].FieldID == id {
				index = i
				break
			}
		}
		if index >= 0 {
			for _, dependency := range definitionDependencies(defs[index]) {
				if dependency == id {
					return fmt.Errorf("derived dependency cycle at %s", id)
				}
				if seen[dependency] {
					if err := visit(dependency); err != nil {
						return err
					}
				}
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for _, definition := range defs {
		if err := visit(definition.FieldID); err != nil {
			return err
		}
	}
	return nil
}

func applyDerivedFields(protocol, purpose string, fields []Field) ([]Field, error) {
	if err := validateDerivedRegistry(derivedDefinitions); err != nil {
		return nil, err
	}
	catalog := fieldMap(fields)
	for _, definition := range derivedDefinitions {
		if !definition.Applicable(protocol, purpose, catalog) {
			continue
		}
		delete(catalog, definition.FieldID)
		fields = append(fields, fillMetadata(definition.Build(catalog, normalizationVersion())))
		catalog[definition.FieldID] = fields[len(fields)-1]
	}
	if err := setDependencyFingerprints(fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func setDependencyFingerprints(fields []Field) error {
	catalog := fieldMap(fields)
	computed := map[string]string{}
	var compute func(id string, visiting map[string]bool) (string, error)
	compute = func(id string, visiting map[string]bool) (string, error) {
		f, ok := catalog[id]
		if !ok {
			return "", fmt.Errorf("missing derived dependency %s", id)
		}
		if value, ok := computed[id]; ok {
			return value, nil
		}
		if visiting[id] {
			return "", fmt.Errorf("derived dependency cycle at %s", id)
		}
		visiting[id] = true
		f = fillMetadata(f)
		scopes := append([]string{}, f.RequiredScopes...)
		sort.Strings(scopes)
		allowed := append([]string{}, f.AllowedOperators...)
		sort.Strings(allowed)
		component := []any{f.FieldID, f.Type, f.Source, f.Pointer, scopes, f.Optional, f.UserEditable, allowed, f.SemanticFormat, f.NormalizationVersion, f.TrustPair}
		dependencies := fieldDependencies(f)
		for _, dependency := range dependencies {
			value, err := compute(dependency, visiting)
			if err != nil {
				return "", err
			}
			component = append(component, dependency, value)
		}
		visiting[id] = false
		value := digest(component)
		computed[id] = value
		for i := range fields {
			if fields[i].FieldID == id {
				fields[i] = f
				break
			}
		}
		return value, nil
	}
	for _, f := range fields {
		if len(fieldDependencies(f)) == 0 {
			continue
		}
		if f.DependencyFingerprint != "" {
			continue
		}
		value, err := compute(f.FieldID, map[string]bool{})
		if err != nil {
			return err
		}
		for i := range fields {
			if fields[i].FieldID == f.FieldID {
				if len(fieldDependencies(fields[i])) != 0 {
					fields[i].DependencyFingerprint = value
				}
			}
		}
	}
	return nil
}

func setField(fields []Field, wanted Field) {
	for i := range fields {
		if fields[i].FieldID == wanted.FieldID {
			fields[i] = fillMetadata(wanted)
			return
		}
	}
	fields = append(fields, fillMetadata(wanted))
}

func sortFieldsByID(fields []Field) []Field {
	ordered := append([]Field{}, fields...)
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j].FieldID < ordered[j-1].FieldID; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	return ordered
}

func baseCatalog(protocol string, mappings []Field) ([]Field, error) {
	if len(mappings) > maxEntries {
		return nil, fmt.Errorf("attribute_mappings: at most %d mappings", maxEntries)
	}
	fields := []Field{
		{FieldID: "external.name", Label: "Name", Type: "string", Source: "oidc_id_token", Pointer: "/name", RequiredScopes: []string{"profile"}, Optional: true, UserEditable: true},
		{FieldID: "external.preferred_username", Label: "Preferred username", Type: "string", Source: "oidc_id_token", Pointer: "/preferred_username", RequiredScopes: []string{"profile"}, Optional: true, UserEditable: true},
		{FieldID: "external.email", Label: "External email (not a platform verified email)", Type: "string", Source: "oidc_id_token", Pointer: "/email", RequiredScopes: []string{"email"}, Optional: true, UserEditable: true},
		{FieldID: "external.email_verified", Label: "External email verified", Type: "boolean", Source: "oidc_id_token", Pointer: "/email_verified", RequiredScopes: []string{"email"}, Optional: true, UserEditable: false, TrustPair: &FieldTrustPair{PeerFieldID: "external.email"}},
	}
	if protocol == "oauth2" || protocol == "password" {
		fields = []Field{}
	} else if protocol != "oidc" && protocol != "password" {
		return nil, fmt.Errorf("unsupported protocol")
	}
	seen, paths := map[string]bool{}, map[string]bool{}
	for _, f := range fields {
		seen[f.FieldID] = true
		paths[f.Source+"\x00"+f.Pointer] = true
	}
	for i, f := range mappings {
		fail := func(message string) ([]Field, error) {
			return nil, fmt.Errorf("attribute_mappings[%d]: %s", i, message)
		}
		oauthEmail := protocol == "oauth2" && f.FieldID == "external.email" && f.Type == "string"
		if !customID.MatchString(f.FieldID) && f.FieldID != "external.account_created_at" && !oauthEmail {
			return fail("field_id must use custom.* or external.account_created_at")
		}
		if seen[f.FieldID] || paths[f.Source+"\x00"+f.Pointer] {
			return fail("duplicate field or ambiguous mapping")
		}
		if !validType(f.Type) || strings.TrimSpace(f.Label) == "" || len(f.Label) > 120 {
			return fail("invalid type or label")
		}
		if len(f.DerivedFrom) != 0 {
			return fail("derived relationships are defined by the server")
		}
		if f.Source == "derived" || f.Source == "registration" {
			return fail("program-defined sources cannot be mapped")
		}
		if len(f.AllowedOperators) != 0 || f.SemanticFormat != "" || f.NormalizationVersion != "" || f.TrustPair != nil || f.DependencyFingerprint != "" {
			return fail("readonly field metadata is program-defined")
		}
		if !ValidSource(protocol, f.Source) {
			return fail("unsupported verified source")
		}
		if _, err := pointerParts(f.Pointer); err != nil {
			return fail(err.Error())
		}
		if len(f.RequiredScopes) > 32 {
			return fail("too many required scopes")
		}
		scopes := map[string]bool{}
		for _, scope := range f.RequiredScopes {
			if scope == "" || len(scope) > 128 || strings.ContainsAny(scope, " \r\n\t") || scopes[scope] {
				return fail("invalid required scope")
			}
			scopes[scope] = true
		}
		if f.FieldID == "external.account_created_at" {
			if f.Type != "datetime" {
				return fail("creation time must be datetime")
			}
		}
		seen[f.FieldID] = true
		paths[f.Source+"\x00"+f.Pointer] = true
		fields = append(fields, f)
	}
	return fields, nil
}

func ValidateForPurpose(protocol string, mappings []Field, purpose string, rules ...RuleSet) error {
	fields, err := CatalogForPurpose(protocol, mappings, purpose)
	if err != nil {
		return err
	}
	catalog := fieldMap(fields)
	for purposeIndex := range rules {
		rule := rules[purposeIndex]
		rule.Conditions = append([]Condition(nil), rule.Conditions...)
		if rule.Mode == "unrestricted" {
			if rule.Match != "" || len(rule.Conditions) != 0 {
				return fmt.Errorf("rules[%d]: unrestricted must not contain conditions", purposeIndex)
			}
			continue
		}
		if rule.Mode != "conditions" || (rule.Match != "all" && rule.Match != "any") || len(rule.Conditions) == 0 || len(rule.Conditions) > maxEntries {
			return fmt.Errorf("rules[%d]: invalid mode, match or condition count", purposeIndex)
		}
		for i, condition := range rule.Conditions {
			f, ok := catalog[condition.FieldID]
			if !ok || !conditionOperatorAllowed(f, condition.Operator) {
				return fmt.Errorf("rules[%d].conditions[%d]: unknown field or invalid operator", purposeIndex, i)
			}
			raw, err := normalizeConditionValue(f, condition)
			if err != nil {
				return fmt.Errorf("rules[%d].conditions[%d].value: %w", purposeIndex, i, err)
			}
			rule.Conditions[i].Value = raw
		}
	}
	return nil
}

func conditionOperatorAllowed(f Field, operator string) bool {
	for _, allowed := range f.AllowedOperators {
		if allowed == operator {
			return true
		}
	}
	return false
}

func normalizeConditionValue(f Field, c Condition) (json.RawMessage, error) {
	v, err := parse(c.Value)
	if err != nil {
		return nil, fmt.Errorf("invalid JSON")
	}
	switch f.Type {
	case "string":
		if f.SemanticFormat == semanticEmailDomain {
			switch c.Operator {
			case "eq", "ne":
				s, ok := v.(string)
				if !ok {
					return nil, fmt.Errorf("expected domain")
				}
				domain, err := accountidentity.NormalizeDomain(s)
				if err != nil {
					return nil, err
				}
				return json.Marshal(domain)
			case "in", "not_in":
				items, ok := v.([]any)
				if !ok || len(items) == 0 {
					return nil, fmt.Errorf("expected 1-100 domains after normalization")
				}
				values := make([]string, 0, len(items))
				for _, item := range items {
					s, ok := item.(string)
					if !ok {
						return nil, fmt.Errorf("expected domain")
					}
					domain, err := accountidentity.NormalizeDomain(s)
					if err != nil {
						return nil, err
					}
					values = append(values, domain)
				}
				values = uniqueSorted(values)
				if len(values) > 100 {
					return nil, fmt.Errorf("expected 1-100 domains after normalization")
				}
				return json.Marshal(values)
			}
		}
		switch c.Operator {
		case "in", "not_in":
			items, ok := v.([]any)
			if !ok || len(items) == 0 {
				return nil, fmt.Errorf("expected non-empty string list")
			}
			values := make([]string, 0, len(items))
			for _, item := range items {
				s, ok := item.(string)
				if !ok || len(s) > 8192 {
					return nil, fmt.Errorf("expected bounded string")
				}
				values = append(values, s)
			}
			values = uniqueSorted(values)
			if len(values) > 1024 {
				return nil, fmt.Errorf("expected at most 1024 distinct strings")
			}
			return json.Marshal(values)
		case "contains", "not_contains", "starts_with", "not_starts_with", "ends_with", "not_ends_with":
			s, ok := v.(string)
			if !ok || len(s) == 0 || len(s) > 8192 {
				return nil, fmt.Errorf("expected non-empty bounded string")
			}
			return c.Value, nil
		default:
			s, ok := v.(string)
			if !ok || len(s) > 8192 {
				return nil, fmt.Errorf("expected bounded string")
			}
			return c.Value, nil
		}
	case "string_list":
		if conditionType(f, c.Operator) == "string_list" {
			items, ok := v.([]any)
			if !ok || len(items) == 0 {
				return nil, fmt.Errorf("expected non-empty string list")
			}
			values := make([]string, 0, len(items))
			for _, item := range items {
				s, ok := item.(string)
				if !ok || len(s) > 8192 {
					return nil, fmt.Errorf("expected bounded string list")
				}
				values = append(values, s)
			}
			values = uniqueSorted(values)
			if len(values) > 1024 {
				return nil, fmt.Errorf("expected at most 1024 distinct strings")
			}
			return json.Marshal(values)
		}
		return typedValueJSON(f.Type, conditionType(f, c.Operator), v)
	default:
		return typedValueJSON(f.Type, f.Type, v)
	}
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func typedValueJSON(t, expectedType string, v any) (json.RawMessage, error) {
	value, err := typed(expectedType, v)
	if err != nil {
		return nil, err
	}
	switch value.(type) {
	case string:
		return json.Marshal(value)
	case bool:
		return json.Marshal(value)
	default:
		return json.Marshal(v)
	}
}

func NormalizeRuleForPurpose(protocol string, mappings []Field, purpose string, rule RuleSet) (RuleSet, error) {
	if err := ValidateForPurpose(protocol, mappings, purpose, rule); err != nil {
		return RuleSet{}, err
	}
	normalized := rule
	normalized.Conditions = append([]Condition(nil), rule.Conditions...)
	fields, _ := CatalogForPurpose(protocol, mappings, purpose)
	catalog := fieldMap(fields)
	for i, condition := range normalized.Conditions {
		f := catalog[condition.FieldID]
		raw, err := normalizeConditionValue(f, condition)
		if err != nil {
			return RuleSet{}, err
		}
		normalized.Conditions[i].Value = append(json.RawMessage(nil), raw...)
	}
	return normalized, nil
}

func ValidateScopesForPurpose(protocol string, mappings []Field, scopes []string, userinfoEnabled bool, purpose string, rules ...RuleSet) error {
	if err := ValidateForPurpose(protocol, mappings, purpose, rules...); err != nil {
		return err
	}
	fields, _ := CatalogForPurpose(protocol, mappings, purpose)
	catalog := fieldMap(fields)
	available := map[string]bool{}
	for _, scope := range scopes {
		available[scope] = true
	}
	for _, id := range RequiredAttributesForPurpose(protocol, mappings, purpose, rules...) {
		f := catalog[id]
		if f.Source == "oidc_userinfo" && !userinfoEnabled {
			return fmt.Errorf("%s: UserInfo is disabled", id)
		}
		for _, scope := range f.RequiredScopes {
			if !available[scope] {
				return fmt.Errorf("%s: required scope %s is missing", id, scope)
			}
		}
	}
	return nil
}

func RequiredAttributesForPurpose(protocol string, mappings []Field, purpose string, rules ...RuleSet) []string {
	fields, err := CatalogForPurpose(protocol, mappings, purpose)
	if err != nil {
		return nil
	}
	catalog := fieldMap(fields)
	return walkCatalogDependencies(catalog, requiredConditionIDs(rules...)...)
}

func RequiredAttributes(mappings []Field, rules ...RuleSet) []string {
	return RequiredAttributesForPurpose(protocolForRequired(mappings, rules...), mappings, purposeSignup, rules...)
}

func protocolForRequired(mappings []Field, rules ...RuleSet) string {
	for _, mapping := range mappings {
		if mapping.Source == "oauth2_profile" {
			return "oauth2"
		}
	}
	for _, rule := range rules {
		for _, condition := range rule.Conditions {
			if strings.HasPrefix(condition.FieldID, "registration.") {
				return "password"
			}
		}
	}
	return "oidc"
}

func RulesFingerprintForPurpose(protocol string, purpose string, rule RuleSet, mappings []Field) string {
	normalized, err := NormalizeRuleForPurpose(protocol, mappings, purpose, rule)
	if err != nil {
		return "invalid"
	}
	fields, _ := CatalogForPurpose(protocol, mappings, purpose)
	catalog := fieldMap(fields)
	fingerprints := []string{}
	for _, id := range RequiredAttributesForPurpose(protocol, mappings, purpose, rule) {
		f, ok := catalog[id]
		if !ok {
			return "invalid"
		}
		fingerprints = append(fingerprints, FingerprintField(f))
	}
	return digest([]any{normalized, fingerprints})
}

func RulesFingerprint(protocol string, rule RuleSet, mappings []Field) string {
	return RulesFingerprintForPurpose(protocol, purposeSignup, rule, mappings)
}

func validSourceForPurpose(protocol, purpose, source string) bool {
	if source == "registration" {
		return purpose == purposeSignup && (protocol == "password" || protocol == "oidc" || protocol == "oauth2")
	}
	return ValidSource(protocol, source)
}

func ExtractForPurpose(protocol string, mappings []Field, purpose string, sources map[string]json.RawMessage, sourceErrors map[string]string, now time.Time) (map[string]Attribute, error) {
	p, err := normalizePurpose(purpose)
	if err != nil {
		return nil, err
	}
	fields, err := CatalogForPurpose(protocol, mappings, p)
	if err != nil {
		return nil, err
	}
	roots := map[string]any{}
	for source, raw := range sources {
		if !validSourceForPurpose(protocol, p, source) {
			return nil, fmt.Errorf("unsupported attribute source")
		}
		root, err := parse(raw)
		if err != nil {
			return nil, err
		}
		if _, ok := root.(map[string]any); !ok {
			return nil, fmt.Errorf("attribute source must be an object")
		}
		roots[source] = root
	}
	out := map[string]Attribute{}
	for _, f := range fields {
		a := Attribute{Status: "unknown", Reason: "missing", Fingerprint: FingerprintField(f)}
		if f.Source == "derived" {
			out[f.FieldID] = a
			continue
		}
		if reason := sourceErrors[f.Source]; reason != "" {
			a.Reason = reason
			out[f.FieldID] = a
			continue
		}
		raw, ok := lookup(roots[f.Source], f.Pointer)
		if !ok {
			out[f.FieldID] = a
			continue
		}
		value, err := canonical(f, raw, now)
		if err != nil {
			a.Reason = "invalid_type"
		} else if f.SemanticFormat == semanticEmail && string(value) == `""` {
			a.Reason = "missing"
		} else {
			a.Value = value
			a.Status = "pass"
			a.Reason = ""
		}
		out[f.FieldID] = a
	}
	registry := derivedDefinitionMap()
	for _, f := range fields {
		if f.Source == "derived" {
			if definition, ok := registry[f.FieldID]; ok {
				out[f.FieldID] = definition.Extract(f, fieldMap(fields), out, sourceErrors, now)
			}
		}
	}
	return out, nil
}

func Extract(protocol string, mappings []Field, sources map[string]json.RawMessage, sourceErrors map[string]string, now time.Time) (map[string]Attribute, error) {
	return ExtractForPurpose(protocol, mappings, purposeSignup, sources, sourceErrors, now)
}

func EvaluateForPurpose(protocol string, purpose string, rule RuleSet, mappings []Field, evidence Evidence, now time.Time) Result {
	out := Result{Status: "unknown", Conditions: []ConditionResult{}}
	normalized, err := NormalizeRuleForPurpose(protocol, mappings, purpose, rule)
	if err != nil {
		return out
	}
	if normalized.Mode == "unrestricted" {
		out.Status = "pass"
		return out
	}
	fields, _ := CatalogForPurpose(protocol, mappings, purpose)
	catalog := fieldMap(fields)
	pass, fail, unknown := 0, 0, 0
	for _, condition := range normalized.Conditions {
		f := catalog[condition.FieldID]
		a, ok := evidence.Attributes[f.FieldID]
		result := ConditionResult{FieldID: f.FieldID, Operator: condition.Operator, Status: "unknown", Reason: "missing"}
		if !evidence.ExpiresAt.IsZero() && !now.Before(evidence.ExpiresAt) {
			result.Reason = "expired"
		} else if !evidence.ObservedAt.IsZero() && evidence.ObservedAt.After(now) {
			result.Reason = "future_observation"
		} else if ok {
			result.Reason = a.Reason
			if a.Fingerprint != FingerprintField(f) {
				result.Reason = "mapping_changed"
			} else if a.Status == "pass" {
				raw, derivedError := derivedEvidence(f, catalog, evidence, now)
				if derivedError != nil {
					result.Reason = derivedError.Error()
				}
				parsed, parseErr := parse(raw)
				actual, typeErr := typed(f.Type, parsed)
				expectedRaw, expectedErr := parse(condition.Value)
				expected, valueErr := typed(conditionType(f, condition.Operator), expectedRaw)
				if derivedError == nil && parseErr == nil && typeErr == nil && expectedErr == nil && valueErr == nil {
					result.Status = "fail"
					result.Reason = ""
					if matches(f, condition.Operator, actual, expected) {
						result.Status = "pass"
					}
				} else if result.Reason == "" {
					result.Reason = "invalid_type"
				}
			}
		}
		switch result.Status {
		case "pass":
			pass++
		case "fail":
			fail++
		default:
			unknown++
			if result.Reason == "source_unavailable" {
				out.Retryable = true
			}
		}
		out.Conditions = append(out.Conditions, result)
	}
	if normalized.Match == "all" {
		if fail > 0 {
			out.Status = "fail"
		} else if unknown == 0 {
			out.Status = "pass"
		}
	} else {
		if pass > 0 {
			out.Status = "pass"
		} else if unknown == 0 {
			out.Status = "fail"
		}
	}
	if out.Status != "unknown" {
		out.Retryable = false
	}
	return out
}

func Evaluate(protocol string, rule RuleSet, mappings []Field, evidence Evidence, now time.Time) Result {
	return EvaluateForPurpose(protocol, purposeSignup, rule, mappings, evidence, now)
}

func derivedEvidence(f Field, catalog map[string]Field, evidence Evidence, now time.Time) (json.RawMessage, error) {
	if f.Source == "derived" {
		if definition, ok := derivedDefinitionMap()[f.FieldID]; ok {
			return definition.Evidence(f, catalog, evidence, now)
		}
		return nil, errors.New("missing_dependency")
	}
	if f.TrustPair != nil && f.TrustPair.PeerFieldID != "" {
		if f.TrustPair.PeerSource != "" && f.TrustPair.PeerSource != f.Source {
			return nil, errors.New("missing_dependency")
		}
		dependency := catalog[f.TrustPair.PeerFieldID]
		base, ok := evidence.Attributes[f.TrustPair.PeerFieldID]
		if !ok || base.Fingerprint != FingerprintField(dependency) || base.Status != "pass" {
			return nil, errors.New("missing_dependency")
		}
	}
	a := evidence.Attributes[f.FieldID]
	return append(json.RawMessage(nil), a.Value...), nil
}

func NormalizeRule(protocol string, mappings []Field, rule RuleSet) (RuleSet, error) {
	return NormalizeRuleForPurpose(protocol, mappings, purposeSignup, rule)
}
