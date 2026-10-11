package api

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
)

// decodeAccountObject preserves field presence and rejects duplicate and unknown
// fields before interpreting their types. Account contracts use exact JSON names.
func (s *Server) decodeAccountObject(w http.ResponseWriter, r *http.Request, allowedFields ...string) (map[string]json.RawMessage, bool) {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体必须是 JSON 对象")
		return nil, false
	}
	fields := make(map[string]json.RawMessage, len(allowedFields))
	for decoder.More() {
		key, err := decoder.Token()
		name, isString := key.(string)
		_, duplicate := fields[name]
		if err != nil || !isString || duplicate || !slices.Contains(allowedFields, name) {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体包含未知字段或重复字段")
			return nil, false
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体不是有效 JSON 对象")
			return nil, false
		}
		fields[name] = value
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体不是有效 JSON 对象")
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体必须仅包含一个 JSON 对象")
		return nil, false
	}
	return fields, true
}

// decodeAccountFields preserves omitted values; only email accepts null, which
// becomes an explicit clear operation for the service's optional string input.
func (s *Server) decodeAccountFields(w http.ResponseWriter, r *http.Request, allowedFields ...string) (map[string]*string, bool) {
	object, ok := s.decodeAccountObject(w, r, allowedFields...)
	if !ok {
		return nil, false
	}
	fields := make(map[string]*string, len(object))
	for name, raw := range object {
		value, ok := s.accountStringValue(w, r, name, raw)
		if !ok {
			return nil, false
		}
		fields[name] = value
	}
	return fields, true
}

func (s *Server) accountStringValue(w http.ResponseWriter, r *http.Request, name string, raw json.RawMessage) (*string, bool) {
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || (value == nil && name != "email") {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求字段必须是字符串，只有邮箱允许 null")
		return nil, false
	}
	if value == nil {
		return new(string), true
	}
	return value, true
}
