package catalog

import (
	_ "embed"
	"encoding/json"
)

// Acceptance records are tracked beside the route catalog, so ignored local
// manuals cannot become the only record of support or retirement obligations.
//
//go:embed acceptance.json
var acceptanceJSON []byte

func Acceptance() json.RawMessage {
	return append(json.RawMessage(nil), acceptanceJSON...)
}
