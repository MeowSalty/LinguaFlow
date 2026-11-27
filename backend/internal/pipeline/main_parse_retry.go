package pipeline

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/repair"
)

const (
	promptUpgradePhase   = "prompt_upgrade"
	promptUpgradePrefix  = responsecontract.PromptUpgradeCursorPrefix
	parseRetryPrefix     = "parse_retry:"
	responsePrefixLabel  = "\nResponse prefix: "
	invalidModelResponse = "invalid model response"
)

type mainRetryKey struct {
	phase    string
	consumed bool
}

func mainRetryFor(cursor WorkCursor) mainRetryKey {
	if strings.HasPrefix(cursor.Phase, promptUpgradePrefix) {
		return mainRetryKey{phase: cursor.Phase, consumed: cursor.NetworkAttempt >= 2}
	}
	if strings.HasPrefix(cursor.Phase, parseRetryPrefix) {
		return mainRetryKey{phase: cursor.Phase}
	}
	return mainRetryKey{}
}

func (r mainRetryKey) parent() string {
	_, parent, _ := strings.Cut(r.phase, ":")
	return parent
}

func (r mainRetryKey) upgrade() bool { return strings.HasPrefix(r.phase, promptUpgradePrefix) }

// IsPendingPromptUpgrade identifies a durable same-logical-attempt retry.
func IsPendingPromptUpgrade(phase string) bool { return strings.HasPrefix(phase, promptUpgradePrefix) }

func (r mainRetryKey) less(other mainRetryKey) bool {
	if r.phase != other.phase {
		return r.phase < other.phase
	}
	return !r.consumed && other.consumed
}

func promptUpgradeEnabled(h stagedHandler, header MainResponseHeader) bool {
	t, ok := h.(*TranslateHandler)
	return ok && t.Repair.PromptUpgrade && t.RetryReminderTemplate != "" && !header.PromptUpgrade
}

func modelParseFailureCursor(round Round, h stagedHandler, header MainResponseHeader) WorkCursor {
	if promptUpgradeEnabled(h, header) {
		return WorkCursor{Pool: header.Pool, Attempt: header.MainAttempt, Phase: promptUpgradePrefix + header.ID, NetworkAttempt: 1, State: "pending"}
	}
	if h.ModeName() == RoundModeRevise && header.MainAttempt < transientBudgetFor(round.Retry) {
		return WorkCursor{Pool: header.Pool, Attempt: header.MainAttempt, Phase: parseRetryPrefix + header.ID, State: "pending"}
	}
	return WorkCursor{Pool: header.Pool + 1, Phase: "initial", State: "pending"}
}

func parseFailureDiagnostic(b *backend.BuildBounds, err error, raw string) (string, error) {
	// The bounded copy contains a concrete reason and a short quoted example.
	// The same readable diagnostic is retained on the completed request ledger.
	const limit = responsecontract.MainParseDiagnosticLimit
	if e := b.ReserveAllocation(4, limit); e != nil {
		return "", e
	}
	reason := strings.Map(func(r rune) rune {
		if r < ' ' {
			return ' '
		}
		return r
	}, diagnosticPrefix(err.Error(), 200))
	return reason + responsePrefixLabel + strconv.Quote(diagnosticPrefix(raw, 200)), nil
}

func diagnosticPrefix(text string, limit int) string {
	if len(text) > limit {
		for limit > 0 && !utf8.RuneStart(text[limit]) {
			limit--
		}
		text = text[:limit]
	}
	return strings.ToValidUTF8(text, "?")
}

func renderMainRetryReminder(b *backend.BuildBounds, template, diagnostic string, limit int64) (string, error) {
	if diagnostic == "" {
		// An older archive has already retired the malformed raw response.
		return repair.RenderRetryReminderBounded(b, template, nil, errors.New(invalidModelResponse), "", limit)
	}
	reason, quoted, ok := strings.Cut(diagnostic, responsePrefixLabel)
	if !ok || reason == "" {
		return "", fmt.Errorf("invalid prompt upgrade diagnostic")
	}
	previous, err := strconv.Unquote(quoted)
	if err != nil {
		return "", fmt.Errorf("invalid prompt upgrade response prefix: %w", err)
	}
	return repair.RenderRetryReminderBounded(b, template, nil, errors.New(reason), previous, limit)
}
