package pipeline

// SegmentInput 表示从 DB 加载的待翻译段落。
type SegmentInput struct {
	// ID 使用 segmentIndex 的字符串形式作为稳定标识。
	// 不使用 hash.Short(sourceText)，因为 DB 主键是 segmentIndex，
	// 且 hash 在 Source 含 protect 占位符时会变化。
	ID         string         // strconv.Itoa(segmentIndex)
	SourceText string         // 原文
	Meta       map[string]any // 从 DB meta 字段反序列化的格式元数据
	TargetText string         // 目标文本（下载渲染时使用）
}

// TranslateSegmentsInput 纯翻译输入，不涉及文件 I/O。
type TranslateSegmentsInput struct {
	// Document 已解析的文档，Segments 中至少需填充 Source 字段。
	// 调用方负责将 DB 数据映射到 Segment。
	Document *Document

	// SegmentIndexes 非空时仅翻译这些索引对应的段落；
	// 未选段落保持原样，已有的 Target 不会被覆盖。
	SegmentIndexes []int

	// ExistingTargets 未选段落的已有译文，用于恢复。
	ExistingTargets map[int]string
}

// BuildDocumentFromSegments 从 DB segments 构建 Document。
// 用于 Web 场景：翻译时从 DB 加载 segments 构建 Document，下载时从 DB + Target 构建 Document。
//
// 字段映射说明：
//   - Segment.ID      → strconv.Itoa(segmentIndex)，与 DB 记录对应
//   - Segment.Source   → seg.SourceText
//   - Segment.OriginalSource → seg.SourceText（从 DB 加载时 Source 就是原文，无 protect 变换）
//   - Segment.Meta     → seg.Meta（从 DB meta 字段反序列化，上传时序列化存入）
//   - Segment.Target   → 翻译前为空；下载渲染时填入 DB 中的 TargetText
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
			Translate:      true,
		}
	}
	return doc
}
