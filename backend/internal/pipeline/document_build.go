package pipeline

import "github.com/MeowSalty/LinguaFlow/backend/internal/qa"

// SegmentInput 表示从 DB 加载的待翻译段落。
type SegmentInput struct {
	// ID 使用 segmentIndex 的字符串形式作为稳定标识。
	// 不使用 hash.Short(sourceText)，因为 DB 主键是 segmentIndex，
	// 且 hash 在 Source 含 protect 占位符时会变化。
	ID         string            // strconv.Itoa(segmentIndex)
	SourceText string            // 原文
	Meta       map[string]any    // 从 DB meta 字段反序列化的格式元数据
	TargetText string            // 目标文本（下载渲染 / 裁决轮使用）
	Issues     []qa.QualityIssue // 质量问题（裁决轮从 DB 重载）
	Status     string            // 段落状态（裁决轮按 translated/edited 筛选）
}

// BuildDocumentFromSegments 从 DB segments 构建 Document。
// 用于 Web 场景：翻译时从 DB 加载 segments 构建 Document，下载时从 DB + Target 构建 Document。
//
// 字段映射说明：
//   - Segment.ID      → strconv.Itoa(segmentIndex)，与 DB 记录对应
//   - Segment.Source   → seg.SourceText
//   - Segment.OriginalSource → seg.SourceText（从 DB 加载时 Source 就是原文，无 protect 变换）
//   - Segment.Meta     → seg.Meta（从 DB meta 字段反序列化，上传时序列化存入）
//   - Segment.Target   → 翻译前为空；下载渲染 / 裁决时填入 DB 中的 TargetText
//   - Segment.Issues   → DB quality_issues（裁决轮使用）
//   - Segment.Status   → DB status（裁决轮筛选）
func BuildDocumentFromSegments(
	segments []SegmentInput,
	sourceLang, targetLang string,
	resourceFormat string,
) *Document {
	doc := &Document{
		SourceLang: sourceLang,
		TargetLang: targetLang,
		Format:     resourceFormat,
		Segments:   make([]Segment, len(segments)),
		Vars:       map[string]any{},
	}
	for i, seg := range segments {
		doc.Segments[i] = Segment{
			ID:             seg.ID,
			Source:         seg.SourceText,
			OriginalSource: seg.SourceText,
			Meta:           seg.Meta,
			Target:         seg.TargetText,
			Issues:         seg.Issues,
			Status:         seg.Status,
			Translate:      true,
		}
	}
	return doc
}
