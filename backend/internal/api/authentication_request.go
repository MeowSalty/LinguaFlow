package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"
)

// Authentication JSON is intentionally exact: encoding/json alone accepts
// duplicate keys, null scalars and case-insensitive field aliases.
func authenticationFields(t reflect.Type) map[string]reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	out := map[string]reflect.Type{}
	if t.Kind() != reflect.Struct {
		return out
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			for k, v := range authenticationFields(f.Type) {
				out[k] = v
			}
			continue
		}
		if f.PkgPath != "" {
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}
func validateAuthenticationJSON(dec *json.Decoder, t reflect.Type, path string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token == nil {
		if path == "client_secret" && t == reflect.TypeFor[json.RawMessage]() {
			return nil
		}
		return errors.New("null authentication field")
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	switch delim {
	case '{':
		fields := authenticationFields(t)
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate authentication field")
			}
			seen[name] = true
			fieldType, ok := fields[name]
			if !ok {
				return errors.New("unknown authentication field")
			}
			fieldPath := name
			if path != "" {
				fieldPath = path + "." + name
			}
			if err := validateAuthenticationJSON(dec, fieldType, fieldPath); err != nil {
				return err
			}
		}
	case '[':
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
			return errors.New("invalid authentication array")
		}
		for dec.More() {
			if err := validateAuthenticationJSON(dec, t.Elem(), path+"[]"); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid authentication JSON")
	}
	_, err = dec.Token()
	return err
}
func (s *Server) decodeExternalRequest(w http.ResponseWriter, r *http.Request, out any) bool {
	defer r.Body.Close()
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "认证请求字段、类型或 JSON 格式不合法")
		return false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		err = validateAuthenticationJSON(dec, reflect.TypeOf(out), "")
		if err == nil {
			if _, tail := dec.Token(); tail != io.EOF {
				err = errors.New("trailing authentication JSON")
			}
		}
	}
	if err == nil {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		err = dec.Decode(out)
	}
	if err != nil {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "认证请求字段、类型或 JSON 格式不合法")
		return false
	}
	return true
}
