package repair

import (
	"bytes"
	"errors"
	"strings"
	"text/template"
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
	result, _ := RenderRetryReminder(DefaultRetryReminderTemplate, missingIDs, parseErr, prevHead)
	return result
}

// DefaultRetryReminderTemplate is frozen into each newly resolved execution.
const DefaultRetryReminderTemplate = `

IMPORTANT: your previous response could not be processed.{{if .Reason}} Reason: {{.Reason}}.{{end}}{{if .MissingIDs}} Missing IDs: {{.MissingIDs}}.{{end}}{{if .PreviousHead}} The previous response started with: {{printf "%q" .PreviousHead}}.{{end}} Reply with EXACTLY the JSON envelope schema described above: {"translations":{"<id>":"<text>",...}}. Do not include markdown fences, prose, or any other fields.`

// RenderRetryReminder applies runtime diagnostic data to the saved template.
func RenderRetryReminder(content string, missingIDs []string, parseErr error, prevHead string) (string, error) {
	if content == "" {
		return "", errors.New("missing frozen retry reminder template")
	}
	t, err := template.New("retry_reminder").Option("missingkey=error").Parse(content)
	if err != nil {
		return "", errors.New("invalid frozen retry reminder template")
	}
	data := struct{ Reason, MissingIDs, PreviousHead string }{MissingIDs: strings.Join(missingIDs, ", "), PreviousHead: prevHead}
	if parseErr != nil {
		data.Reason = parseErr.Error()
	}
	if len(data.PreviousHead) > 200 {
		data.PreviousHead = data.PreviousHead[:200] + "…"
	}
	var out bytes.Buffer
	if err := t.Execute(&out, data); err != nil {
		return "", errors.New("render frozen retry reminder template failed")
	}
	return out.String(), nil
}
