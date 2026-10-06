package v013

import "encoding/json"

// UnmarshalJSON preserves v0.13.0's handling of a known historical field.
// Its reader ignored translate.strategy and executed only the top-level
// strategy. Discard the legacy value here while keeping every other field
// strict; it must never enter the frozen source value or target provenance.
func (snapshot *sourceJobTranslateRoundSnapshot) UnmarshalJSON(raw []byte) error {
	type plain sourceJobTranslateRoundSnapshot
	var decoded struct {
		plain
		IgnoredStrategy json.RawMessage `json:"strategy"`
	}
	if err := decodeSource(raw, &decoded); err != nil {
		return err
	}
	*snapshot = sourceJobTranslateRoundSnapshot(decoded.plain)
	return nil
}
