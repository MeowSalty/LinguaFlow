package progress

import (
	"strings"
	"unicode/utf8"
)

// MaxSSEBatchContentBytes is the per-field limit for sent/received content in batch SSE metadata.
const MaxSSEBatchContentBytes = 128 * 1024

// TruncateSSEContent truncates s to at most MaxSSEBatchContentBytes for SSE payloads.
// Truncation never splits a UTF-8 code point.
func TruncateSSEContent(s string) (content string, truncated bool, originalLen int) {
	return BatchEventContent(s, 0)
}

// BatchEventContent keeps the original length of a previously capped field.
func BatchEventContent(s string, originalLen int) (content string, truncated bool, length int) {
	prefix := SSEContentPrefix(s)
	length = max(originalLen, len(s))
	return strings.Clone(prefix), len(prefix) < length, length
}

// SSEContentPrefix returns the displayable prefix without allocating a copy.
func SSEContentPrefix(s string) string {
	if len(s) <= MaxSSEBatchContentBytes {
		return s
	}
	end := MaxSSEBatchContentBytes
	if !utf8.ValidString(s[:end]) {
		for i := 0; i < end; {
			r, size := utf8.DecodeRuneInString(s[i:end])
			if r == utf8.RuneError && size == 1 {
				return s[:i]
			}
			i += size
		}
	}
	return s[:end]
}

// BatchLevelFromStatus maps batch metadata status to SSE level.
func BatchLevelFromStatus(status string) string {
	switch status {
	case "success":
		return "info"
	case "partial":
		return "warn"
	case "failed":
		return "error"
	default:
		return "info"
	}
}
