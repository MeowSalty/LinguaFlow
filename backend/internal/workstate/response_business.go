package workstate

import (
	"bytes"

	"encoding/hex"
	"encoding/json"

	"io"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

const responseReceiptReadBytes = 2 << 10

func validateResponseReceipt(data []byte, requestID string) error {
	if len(data) == 0 {
		return nil
	}
	if len(data) > responseReceiptReadBytes || !utf8.Valid(data) || !utf8.ValidString(requestID) {
		return ErrManifest
	}
	var compact glossary.ResponseReceipt
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&compact); err != nil {
		return ErrManifest
	}
	var extra json.RawMessage
	if err := d.Decode(&extra); err != io.EOF {
		return ErrManifest
	}
	outcome, err := hex.DecodeString(compact.OutcomeDigest)
	if compact.Version != glossary.ResponseReceiptVersion || compact.RequestID != requestID || len(compact.RequestID) == 0 || len(compact.RequestID) > 128 || !utf8.ValidString(compact.RequestID) || len(compact.InputDigest) == 0 || len(compact.InputDigest) > 128 || !utf8.ValidString(compact.InputDigest) || len(compact.OutcomeDigest) != 64 || err != nil || len(outcome) != 32 || compact.Added < 0 || compact.Skipped < 0 {
		return ErrManifest
	}
	return nil
}
