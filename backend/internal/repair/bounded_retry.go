package repair

import (
	"errors"
	"reflect"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
)

// Frozen diagnostic errors are read as stored strings, without invoking an
// arbitrary Error callback before a credit check. Standard errors/fmt wrappers
// already retain the message; capacity errors have two finite string fields.
func retryReasonBounded(b *backend.BuildBounds, err error, maxBytes int64) (string, error) {
	if err == nil {
		return "", nil
	}
	if e, ok := err.(*backend.BuildCapacityError); ok {
		if int64(len(e.Resource))+int64(len(backend.ErrBuildCapacity.Error()))+2 > maxBytes {
			return "", &backend.BuildCapacityError{Resource: "retry reason bytes"}
		}
		return joinFragmentsBounded(b, []string{backend.ErrBuildCapacity.Error(), e.Resource}, ": ", maxBytes)
	}
	v := reflect.ValueOf(err)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return "", errors.New("unsupported retry diagnostic callback")
	}
	v = v.Elem()
	t := v.Type()
	if (t.PkgPath() == "errors" && t.Name() == "errorString") || (t.PkgPath() == "fmt" && (t.Name() == "wrapError" || t.Name() == "wrapErrors")) {
		field := "msg"
		if t.PkgPath() == "errors" {
			field = "s"
		}
		s := v.FieldByName(field).String()
		if int64(len(s)) > maxBytes {
			return "", &backend.BuildCapacityError{Resource: "retry reason bytes"}
		}
		return s, nil
	}
	return "", errors.New("unsupported retry diagnostic callback")
}
func RenderRetryReminderBounded(b *backend.BuildBounds, content string, missingIDs []string, parseErr error, prevHead string, maxBytes int64) (string, error) {
	if content == "" {
		return "", errors.New("missing frozen retry reminder template")
	}
	if e := b.ReserveNodes(8, 1024); e != nil {
		return "", e
	}
	ids, e := joinFragmentsBounded(b, missingIDs, ", ", maxBytes)
	if e != nil {
		return "", e
	}
	reason, e := retryReasonBounded(b, parseErr, maxBytes)
	if e != nil {
		return "", e
	}
	if len(prevHead) > 200 {
		prevHead, e = joinFragmentsBounded(b, []string{prevHead[:200], "…"}, "", maxBytes)
		if e != nil {
			return "", e
		}
	}
	data := struct{ Reason, MissingIDs, PreviousHead string }{reason, ids, prevHead}
	return prompt.RenderTemplateBounded(b, content, data, maxBytes)
}
