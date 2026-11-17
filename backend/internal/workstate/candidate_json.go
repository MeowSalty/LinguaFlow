package workstate

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"sync"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

// CandidateReadBytes bounds the database's JSON representation independently
// of the canonical DTO's Q. JSONB whitespace and numeric spelling may differ.
const CandidateReadBytes int64 = 64 << 20

func CanonicalCandidateJSON(data []byte, limit int64, contexts ...context.Context) ([]byte, error) {
	payload, release, err := canonicalCandidateJSONOwned(data, limit, contexts...)
	if release != nil {
		release()
	}
	return payload, err
}

func canonicalCandidateJSONOwned(data []byte, limit int64, contexts ...context.Context) ([]byte, func(), error) {
	if int64(len(data)) > CandidateReadBytes || limit <= 0 {
		return nil, nil, backend.ErrBuildCapacity
	}
	ctx := context.Background()
	if len(contexts) > 0 {
		ctx = contexts[0]
	}
	b, err := backend.NewBuildBoundsFromContext(ctx, limit, int64(len(data)))
	if err != nil {
		return nil, nil, err
	}
	var value any
	ok := false
	defer func() {
		if !ok {
			value = nil
			b.CloseRetired()
		}
	}()
	if err = b.DecodeJSONLimited(data, &value, CandidateReadBytes); err != nil {
		return nil, nil, err
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
		return nil, nil, err
	}
	payload, err := b.EncodeJSON(value, limit)
	if err != nil {
		return nil, nil, err
	}
	var once sync.Once
	ok = true
	return payload, func() { once.Do(func() { value = nil; payload = nil; b.CloseRetired() }) }, nil
}
