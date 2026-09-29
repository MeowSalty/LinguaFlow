package api

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
)

// decodeAccountFields preserves omitted strings while rejecting explicit nulls.
// Account updates use this stricter contract without changing other handlers.
func (s *Server) decodeAccountFields(w http.ResponseWriter, r *http.Request, allowedFields ...string) (map[string]*string, bool) {
	defer r.Body.Close()
	decoder := json.NewDecoder(r.Body)
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体必须是 JSON 对象，字段值必须是非 null 字符串")
		return nil, false
	}
	fields := make(map[string]*string, len(allowedFields))
	for decoder.More() {
		key, err := decoder.Token()
		name, isString := key.(string)
		_, duplicate := fields[name]
		if err != nil || !isString || duplicate || !slices.Contains(allowedFields, name) {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求体包含未知字段或重复字段")
			return nil, false
		}
		var value *string
		if err := decoder.Decode(&value); err != nil || value == nil {
			s.writeProblem(w, r, http.StatusBadRequest, "invalid_request", "请求字段必须是非 null 字符串")
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
