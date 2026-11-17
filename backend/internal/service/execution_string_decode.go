package service

import "github.com/MeowSalty/LinguaFlow/backend/internal/backend"

// The SQL text remains borrowed. Only the temporary string-to-byte conversion
// is owned here, and it ends after the decoder finishes reading that slice.
func decodeExecutionString(b *backend.BuildBounds, text string, target any, limit int64) error {
	owner, err := b.OwnAllocation(int64(len(text)), 1)
	if err != nil {
		return err
	}
	data := []byte(text)
	defer func() { data = nil; owner.Release() }()
	return b.DecodeJSONLimited(data, target, limit)
}
