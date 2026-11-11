package workstate

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// CandidateReadBytes bounds the database's JSON representation independently
// of the canonical DTO's Q. JSONB whitespace and numeric spelling may differ.
const CandidateReadBytes int64 = 64 << 20

func CanonicalCandidateJSON(data []byte, limit int64) ([]byte, error) {
	if int64(len(data)) > CandidateReadBytes || limit <= 0 {
		return nil, backend.ErrBuildCapacity
	}
	b, err := backend.NewBuildBounds(limit, int64(len(data)))
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err = b.DecodeJSONLimited(data, &raw, CandidateReadBytes); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err = decoder.Decode(&value); err != nil {
		return nil, err
	}
	var normalize func(any) (any, error)
	normalize = func(v any) (any, error) {
		switch v := v.(type) {
		case json.Number:
			if n, e := strconv.ParseInt(string(v), 10, 64); e == nil {
				return n, nil
			}
			if n, e := strconv.ParseUint(string(v), 10, 64); e == nil {
				return n, nil
			}
			n, e := strconv.ParseFloat(string(v), 64)
			if e != nil || math.IsInf(n, 0) || math.IsNaN(n) {
				return nil, backend.ErrBuildCapacity
			}
			return n, nil
		case map[string]any:
			for k, n := range v {
				v[k], err = normalize(n)
				if err != nil {
					return nil, err
				}
			}
			return v, nil
		case []any:
			for i, n := range v {
				v[i], err = normalize(n)
				if err != nil {
					return nil, err
				}
			}
			return v, nil
		default:
			return v, nil
		}
	}
	value, err = normalize(value)
	if err != nil {
		return nil, err
	}
	return b.EncodeJSON(value, limit)
}
