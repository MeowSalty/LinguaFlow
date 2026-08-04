package pipeline

import "github.com/MeowSalty/LinguaFlow/backend/internal/model"

// Segment 是文档中一个可翻译的最小单元。
// 类型别名，实际定义在 model 包中。
type Segment = model.Segment

// Document 是 parser 解析后的中间表示。stages 在其上原地修改。
// 类型别名，实际定义在 model 包中。
type Document = model.Document
