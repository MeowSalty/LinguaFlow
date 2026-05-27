package repair

import (
	"fmt"
	"strings"
)

// BuildRetryReminder 生成反例式 reminder，用于 L4 升级重试。
// 调用方应把返回内容追加到 system prompt 末尾，让 LLM 看到具体错误并按 schema 重答。
//
// 参数：
//   - missingIDs：本轮缺失的 ID 列表；空切片表示不是 ID 缺失问题（如 JSON 解析失败）
//   - parseErr：上次解析失败的具体原因；nil 时不输出
//   - prevHead：上次响应前若干字符的截断（≤200），用作反例；空串时不输出
//
// 注意：不要 echo 完整的破损 JSON——会让模型可能继续延续错误，token 也吃不消。
func BuildRetryReminder(missingIDs []string, parseErr error, prevHead string) string {
	var b strings.Builder
	b.WriteString("\n\nIMPORTANT: your previous response could not be processed.")
	if parseErr != nil {
		b.WriteString(" Reason: ")
		b.WriteString(parseErr.Error())
		b.WriteString(".")
	}
	if len(missingIDs) > 0 {
		b.WriteString(fmt.Sprintf(" Missing IDs: %s.", strings.Join(missingIDs, ", ")))
	}
	if prevHead != "" {
		head := prevHead
		if len(head) > 200 {
			head = head[:200] + "…"
		}
		b.WriteString(fmt.Sprintf(" The previous response started with: %q.", head))
	}
	b.WriteString(` Reply with EXACTLY the JSON envelope schema described above: ` +
		`{"translations":{"<id>":"<text>",...}}. ` +
		`Do not include markdown fences, prose, or any other fields.`)
	return b.String()
}
