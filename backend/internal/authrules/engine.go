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
		switch op {
		case "eq", "ne", "in", "not_in", "contains", "not_contains",
			"starts_with", "not_starts_with", "ends_with", "not_ends_with":
			return true
		}
		return false
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
	if op == "in" || op == "not_in" || op == "contains_any" || op == "contains_all" {
		return "string_list"
	}
	if op == "contains" || op == "not_contains" {
		return "string"
	}
	return f.Type
}
func digest(v any) string {
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func FingerprintField(f Field) string {
	f = fillMetadata(f)
	scopes := append([]string{}, f.RequiredScopes...)
	sort.Strings(scopes)
	allowed := append([]string{}, f.AllowedOperators...)
	sort.Strings(allowed)
	derived := append([]string{}, f.DerivedFrom...)
	sort.Strings(derived)
	return digest([]any{f.FieldID, f.Type, f.Source, f.Pointer, scopes, f.Optional, f.UserEditable, derived, allowed, f.SemanticFormat, f.NormalizationVersion, f.TrustPair, f.DependencyFingerprint})
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
		switch op {
		case "in":
			return contains(expected.([]string), a)
		case "not_in":
			return !contains(expected.([]string), a)
		case "contains":
			return strings.Contains(a, expected.(string))
		case "not_contains":
			return !strings.Contains(a, expected.(string))
		case "starts_with":
			return strings.HasPrefix(a, expected.(string))
		case "not_starts_with":
			return !strings.HasPrefix(a, expected.(string))
		case "ends_with":
			return strings.HasSuffix(a, expected.(string))
		case "not_ends_with":
			return !strings.HasSuffix(a, expected.(string))
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

func ValidSource(protocol, source string) bool {
	switch protocol {
	case "oidc":
		return source == "oidc_id_token" || source == "oidc_userinfo"
	case "oauth2":
		return source == "oauth2_profile"
	}
	return false
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
