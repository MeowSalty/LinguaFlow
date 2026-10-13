package authrules

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxEntries = 64

var customID = regexp.MustCompile(`^custom\.[a-z][a-z0-9_.-]{0,119}$`)

// ValidateJSON rejects ambiguous objects before encoding/json can discard keys.
func ValidateJSON(data []byte) error {
	if len(data) > 1<<20 || !utf8.Valid(data) {
		return fmt.Errorf("invalid JSON size or encoding")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	budget := 100000
	var walk func(int) error
	walk = func(depth int) error {
		budget--
		if depth > 32 || budget < 0 {
			return fmt.Errorf("JSON nesting or size exceeds limit")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return fmt.Errorf("duplicate JSON property")
				}
				seen[name] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("invalid JSON container")
		}
		_, err = d.Token()
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}
func parse(data []byte) (any, error) {
	if err := ValidateJSON(data); err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var v any
	err := d.Decode(&v)
	return v, err
}
func pointerParts(pointer string) ([]string, error) {
	if len(pointer) == 0 || len(pointer) > 1024 || pointer[0] != '/' {
		return nil, fmt.Errorf("pointer must name a JSON field")
	}
	parts := strings.Split(pointer[1:], "/")
	if len(parts) > 16 {
		return nil, fmt.Errorf("pointer too deep")
	}
	for i, part := range parts {
		var b strings.Builder
		for j := 0; j < len(part); j++ {
			if part[j] != '~' {
				b.WriteByte(part[j])
				continue
			}
			j++
			if j >= len(part) {
				return nil, fmt.Errorf("invalid pointer escape")
			}
			switch part[j] {
			case '0':
				b.WriteByte('~')
			case '1':
				b.WriteByte('/')
			default:
				return nil, fmt.Errorf("invalid pointer escape")
			}
		}
		parts[i] = b.String()
	}
	return parts, nil
}
func lookup(root any, pointer string) (any, bool) {
	parts, err := pointerParts(pointer)
	if err != nil {
		return nil, false
	}
	v := root
	for _, part := range parts {
		switch current := v.(type) {
		case map[string]any:
			var ok bool
			v, ok = current[part]
			if !ok {
				return nil, false
			}
		case []any:
			if part == "" || (len(part) > 1 && part[0] == '0') {
				return nil, false
			}
			for _, r := range part {
				if r < '0' || r > '9' {
					return nil, false
				}
			}
			index, err := strconv.Atoi(part)
			if err != nil || index >= len(current) {
				return nil, false
			}
			v = current[index]
		default:
			return nil, false
		}
	}
	return v, true
}
func validType(t string) bool {
	switch t {
	case "string", "boolean", "integer", "number", "datetime", "string_list":
		return true
	}
	return false
}
func Catalog(protocol string, mappings []Field) ([]Field, error) {
	if len(mappings) > maxEntries {
		return nil, fmt.Errorf("attribute_mappings: at most %d mappings", maxEntries)
	}
	fields := []Field{
		{FieldID: "external.name", Label: "Name", Type: "string", Source: "oidc_id_token", Pointer: "/name", RequiredScopes: []string{"profile"}, Optional: true, UserEditable: true},
		{FieldID: "external.preferred_username", Label: "Preferred username", Type: "string", Source: "oidc_id_token", Pointer: "/preferred_username", RequiredScopes: []string{"profile"}, Optional: true, UserEditable: true},
		{FieldID: "external.email", Label: "External email (not a platform verified email)", Type: "string", Source: "oidc_id_token", Pointer: "/email", RequiredScopes: []string{"email"}, Optional: true, UserEditable: true},
	}
	if protocol == "oauth2" {
		fields = []Field{}
	} else if protocol != "oidc" && protocol != "password" {
		return nil, fmt.Errorf("unsupported protocol")
	}
	seen, paths := map[string]bool{}, map[string]bool{}
	for _, f := range fields {
		seen[f.FieldID] = true
		paths[f.Source+"\x00"+f.Pointer] = true
	}
	created := false
	for i, f := range mappings {
		fail := func(message string) ([]Field, error) {
			return nil, fmt.Errorf("attribute_mappings[%d]: %s", i, message)
		}
		if !customID.MatchString(f.FieldID) && f.FieldID != "external.account_created_at" {
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
			created = true
		}
		seen[f.FieldID] = true
		paths[f.Source+"\x00"+f.Pointer] = true
		fields = append(fields, f)
	}
	if created {
		fields = append(fields, Field{FieldID: "external.account_age_days", Label: "Account age (days)", Type: "integer", Source: "derived", Pointer: "external.account_created_at", Optional: true, DerivedFrom: []string{"external.account_created_at"}})
	}
	return fields, nil
}
func fieldMap(fields []Field) map[string]Field {
	out := make(map[string]Field, len(fields))
	for _, f := range fields {
		out[f.FieldID] = f
	}
	return out
}
func allowedOperator(t, op string) bool {
	switch t {
	case "string":
		return op == "eq" || op == "ne" || op == "in"
	case "boolean":
		return op == "eq"
	case "number", "integer":
		return op == "eq" || op == "ne" || op == "gt" || op == "gte" || op == "lt" || op == "lte"
	case "datetime":
		return op == "gt" || op == "gte" || op == "lt" || op == "lte"
	case "string_list":
		return op == "contains" || op == "not_contains" || op == "contains_any" || op == "contains_all"
	}
	return false
}
func number(v any, integer bool) (*big.Rat, error) {
	n, ok := v.(json.Number)
	if !ok || len(n) > 128 {
		return nil, fmt.Errorf("expected number")
	}
	if integer {
		z, ok := new(big.Int).SetString(string(n), 10)
		if !ok || z.BitLen() > 256 {
			return nil, fmt.Errorf("expected bounded integer")
		}
		return new(big.Rat).SetInt(z), nil
	}
	// Bound the exponent before constructing a rational, preventing huge powers.
	text := string(n)
	if i := strings.IndexAny(text, "eE"); i >= 0 {
		exp, err := strconv.Atoi(text[i+1:])
		if err != nil || exp > 308 || exp < -308 {
			return nil, fmt.Errorf("number exponent out of range")
		}
	}
	r, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil, fmt.Errorf("invalid number")
	}
	return r, nil
}
func typed(t string, v any) (any, error) {
	switch t {
	case "string":
		s, ok := v.(string)
		if ok && len(s) <= 8192 {
			return s, nil
		}
	case "boolean":
		b, ok := v.(bool)
		if ok {
			return b, nil
		}
	case "integer", "number":
		return number(v, t == "integer")
	case "datetime":
		s, ok := v.(string)
		if ok {
			when, err := time.Parse(time.RFC3339Nano, s)
			if err == nil {
				return when.UTC(), nil
			}
		}
	case "string_list":
		items, ok := v.([]any)
		if ok && len(items) <= 1024 {
			out := make([]string, len(items))
			for i, item := range items {
				s, ok := item.(string)
				if !ok || len(s) > 8192 {
					return nil, fmt.Errorf("invalid string list")
				}
				out[i] = s
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("expected %s", t)
}
func conditionType(f Field, op string) string {
	if op == "in" || op == "contains_any" || op == "contains_all" {
		return "string_list"
	}
	if op == "contains" || op == "not_contains" {
		return "string"
	}
	return f.Type
}
func Validate(protocol string, mappings []Field, rules ...RuleSet) error {
	fields, err := Catalog(protocol, mappings)
	if err != nil {
		return err
	}
	catalog := fieldMap(fields)
	for purpose, rule := range rules {
		if rule.Mode == "unrestricted" {
			if rule.Match != "" || len(rule.Conditions) != 0 {
				return fmt.Errorf("rules[%d]: unrestricted must not contain conditions", purpose)
			}
			continue
		}
		if rule.Mode != "conditions" || (rule.Match != "all" && rule.Match != "any") || len(rule.Conditions) == 0 || len(rule.Conditions) > maxEntries {
			return fmt.Errorf("rules[%d]: invalid mode, match or condition count", purpose)
		}
		for i, condition := range rule.Conditions {
			f, ok := catalog[condition.FieldID]
			if !ok || !allowedOperator(f.Type, condition.Operator) {
				return fmt.Errorf("rules[%d].conditions[%d]: unknown field or invalid operator", purpose, i)
			}
			v, err := parse(condition.Value)
			if err != nil {
				return fmt.Errorf("rules[%d].conditions[%d].value: invalid JSON", purpose, i)
			}
			if _, err := typed(conditionType(f, condition.Operator), v); err != nil {
				return fmt.Errorf("rules[%d].conditions[%d].value: %w", purpose, i, err)
			}
		}
	}
	return nil
}
func ValidateScopes(protocol string, mappings []Field, scopes []string, userinfoEnabled bool, rules ...RuleSet) error {
	if err := Validate(protocol, mappings, rules...); err != nil {
		return err
	}
	fields, _ := Catalog(protocol, mappings)
	catalog := fieldMap(fields)
	available := map[string]bool{}
	for _, scope := range scopes {
		available[scope] = true
	}
	for _, id := range RequiredAttributes(mappings, rules...) {
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
func digest(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func FingerprintField(f Field) string {
	scopes := append([]string{}, f.RequiredScopes...)
	sort.Strings(scopes)
	return digest([]any{f.FieldID, f.Type, f.Source, f.Pointer, scopes, f.Optional, f.UserEditable})
}
func RequiredAttributes(mappings []Field, rules ...RuleSet) []string {
	seen := map[string]bool{}
	for _, rule := range rules {
		for _, condition := range rule.Conditions {
			seen[condition.FieldID] = true
			if condition.FieldID == "external.account_age_days" {
				seen["external.account_created_at"] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
func RulesFingerprint(protocol string, rule RuleSet, mappings []Field) string {
	fields, err := Catalog(protocol, mappings)
	if err != nil {
		return "invalid"
	}
	catalog := fieldMap(fields)
	fingerprints := []string{}
	for _, id := range RequiredAttributes(mappings, rule) {
		f, ok := catalog[id]
		if !ok {
			return "invalid"
		}
		fingerprints = append(fingerprints, FingerprintField(f))
	}
	return digest([]any{rule, fingerprints})
}
func SelectAttributes(attrs map[string]Attribute, ids []string) map[string]Attribute {
	out := map[string]Attribute{}
	for _, id := range ids {
		if v, ok := attrs[id]; ok {
			v.Value = append(json.RawMessage(nil), v.Value...)
			out[id] = v
		}
	}
	return out
}
func canonical(f Field, raw any, now time.Time) (json.RawMessage, error) {
	v, err := typed(f.Type, raw)
	if err != nil {
		return nil, err
	}
	if when, ok := v.(time.Time); ok {
		if f.FieldID == "external.account_created_at" && when.After(now) {
			return nil, fmt.Errorf("future creation time")
		}
		return json.Marshal(when.Format(time.RFC3339Nano))
	}
	// Preserve the exact JSON number rather than serializing big.Rat as a string.
	if _, ok := v.(*big.Rat); ok {
		return json.Marshal(raw)
	}
	return json.Marshal(v)
}
func Extract(protocol string, mappings []Field, sources map[string]json.RawMessage, sourceErrors map[string]string, now time.Time) (map[string]Attribute, error) {
	fields, err := Catalog(protocol, mappings)
	if err != nil {
		return nil, err
	}
	roots := map[string]any{}
	for source, raw := range sources {
		if !ValidSource(protocol, source) {
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
		} else {
			a.Value = value
			a.Status = "pass"
			a.Reason = ""
		}
		out[f.FieldID] = a
	}
	if created, ok := out["external.account_created_at"]; ok {
		age := out["external.account_age_days"]
		age.Status, age.Reason = created.Status, created.Reason
		if created.Status == "pass" {
			var value string
			_ = json.Unmarshal(created.Value, &value)
			when, _ := time.Parse(time.RFC3339Nano, value)
			age.Value = json.RawMessage(strconv.FormatInt(accountAgeDays(now, when), 10))
		}
		out["external.account_age_days"] = age
	}
	return out, nil
}

// RFC3339 dates can span more than time.Duration's approximately 292 years.
func accountAgeDays(now, created time.Time) int64 {
	seconds := now.Unix() - created.Unix()
	if now.Nanosecond() < created.Nanosecond() {
		seconds--
	}
	return seconds / 86400
}
func compare(op string, cmp int) bool {
	switch op {
	case "eq":
		return cmp == 0
	case "ne":
		return cmp != 0
	case "gt":
		return cmp > 0
	case "gte":
		return cmp >= 0
	case "lt":
		return cmp < 0
	case "lte":
		return cmp <= 0
	}
	return false
}
func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}
func matches(f Field, op string, actual, expected any) bool {
	switch f.Type {
	case "string":
		a := actual.(string)
		if op == "in" {
			return contains(expected.([]string), a)
		}
		return compare(op, strings.Compare(a, expected.(string)))
	case "boolean":
		return actual.(bool) == expected.(bool)
	case "integer", "number":
		return compare(op, actual.(*big.Rat).Cmp(expected.(*big.Rat)))
	case "datetime":
		return compare(op, actual.(time.Time).Compare(expected.(time.Time)))
	case "string_list":
		items := actual.([]string)
		if op == "contains" {
			return contains(items, expected.(string))
		}
		if op == "not_contains" {
			return !contains(items, expected.(string))
		}
		if op == "contains_any" {
			for _, v := range expected.([]string) {
				if contains(items, v) {
					return true
				}
			}
			return false
		}
		for _, v := range expected.([]string) {
			if !contains(items, v) {
				return false
			}
		}
		return true
	}
	return false
}
func Evaluate(protocol string, rule RuleSet, mappings []Field, evidence Evidence, now time.Time) Result {
	out := Result{Status: "unknown", Conditions: []ConditionResult{}}
	if err := Validate(protocol, mappings, rule); err != nil {
		return out
	}
	if rule.Mode == "unrestricted" {
		out.Status = "pass"
		return out
	}
	fields, _ := Catalog(protocol, mappings)
	catalog := fieldMap(fields)
	pass, fail, unknown := 0, 0, 0
	for _, condition := range rule.Conditions {
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
				raw, err := parse(a.Value)
				if f.Source == "derived" {
					created, exists := evidence.Attributes["external.account_created_at"]
					cf := catalog["external.account_created_at"]
					if !exists || created.Fingerprint != FingerprintField(cf) || created.Status != "pass" {
						err = fmt.Errorf("creation evidence unavailable")
						result.Reason = "missing_dependency"
					} else {
						var value string
						if json.Unmarshal(created.Value, &value) != nil {
							err = fmt.Errorf("invalid creation time")
						} else {
							when, e := time.Parse(time.RFC3339Nano, value)
							if e != nil || when.After(now) {
								err = fmt.Errorf("invalid creation time")
							} else {
								raw = json.Number(strconv.FormatInt(accountAgeDays(now, when), 10))
								err = nil
							}
						}
					}
				}
				actual, typeErr := typed(f.Type, raw)
				expectedRaw, expectedErr := parse(condition.Value)
				expected, valueErr := typed(conditionType(f, condition.Operator), expectedRaw)
				if err == nil && typeErr == nil && expectedErr == nil && valueErr == nil {
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
	if rule.Match == "all" {
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

func ValidSource(protocol, source string) bool {
	return protocol == "oidc" && (source == "oidc_id_token" || source == "oidc_userinfo") || protocol == "oauth2" && source == "oauth2_profile"
}

// Subject extraction uses the same bounded JSON Pointer grammar as ordinary mappings.
func ValidatePointer(pointer string) error { _, err := pointerParts(pointer); return err }
func JSONValue(raw []byte, pointer string) (any, bool) {
	root, err := parse(raw)
	if err != nil {
		return nil, false
	}
	return lookup(root, pointer)
}
