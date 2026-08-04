package progress

import "unicode/utf8"

// MaxSSEBatchContentBytes is the per-field limit for sent/received content in batch SSE metadata.
const MaxSSEBatchContentBytes = 128 * 1024

// TruncateSSEContent truncates s to at most MaxSSEBatchContentBytes for SSE payloads.
// Truncation never splits a UTF-8 code point.
func TruncateSSEContent(s string) (content string, truncated bool, originalLen int) {
	originalLen = len(s)
	if originalLen <= MaxSSEBatchContentBytes {
		return s, false, originalLen
	}
	end := MaxSSEBatchContentBytes
	for end > 0 && !utf8.ValidString(s[:end]) {
		end--
	}
	if end == 0 {
		return "", true, originalLen
	}
	return s[:end], true, originalLen
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
