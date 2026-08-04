package subtitle

import (
	"regexp"
	"strings"
)

// timestampRE 匹配 SRT（逗号）与 VTT（点号）两种时间戳行。
var timestampRE = regexp.MustCompile(
	`^\d{2}:\d{2}:\d{2}[,\.]\d{3}\s*-->\s*\d{2}:\d{2}:\d{2}[,\.]\d{3}`,
)

// splitBlocks 将文本按一个（或以上）空行分割为若干个 block。
func splitBlocks(text string) []string {
	blocks := strings.Split(text, "\n\n")
	// 移除全空 block
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if strings.TrimSpace(b) != "" {
			out = append(out, b)
		}
	}
	return out
}

// findTimestampLine 在 lines 中查找第一个匹配时间戳格式的行，返回其索引。
// 未找到时返回 -1。
func findTimestampLine(lines []string) int {
	for i, l := range lines {
		if timestampRE.MatchString(strings.TrimSpace(l)) {
			return i
		}
	}
	return -1
}

// extractStart 从 "HH:MM:SS,mmm --> HH:MM:SS,mmm" 中提取起始时间。
func extractStart(timestampLine string) string {
	idx := strings.Index(timestampLine, "-->")
	if idx < 0 {
		return timestampLine
	}
	return strings.TrimSpace(timestampLine[:idx])
}

// extractEnd 从时间戳行中提取结束时间。
func extractEnd(timestampLine string) string {
	idx := strings.Index(timestampLine, "-->")
	if idx < 0 {
		return ""
	}
	after := timestampLine[idx+3:]
	// 结束时间后可能跟随 VTT cue settings，以空格分隔
	parts := strings.Fields(after)
	if len(parts) > 0 {
		return parts[0]
	}
	return strings.TrimSpace(after)
}

// joinNonEmpty 连接非空字符串，以 sep 分隔。
func joinNonEmpty(lines []string, sep string) string {
	var b strings.Builder
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(sep)
		}
		b.WriteString(t)
	}
	return b.String()
}
