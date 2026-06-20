// Package engine 组合 parser、pipeline、backend，对外暴露 Translate 入口。
package engine

// 通过空白导入触发各子包 init() 自注册到 parser/backend 全局表。
// CLI 层只需 import "engine"，无需逐个 import 各 parser / backend。
import (
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/backend/anthropic"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/backend/google"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/backend/openai"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/parser/epub"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/parser/jsonp"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/parser/markdown"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/parser/subtitle"
	_ "github.com/MeowSalty/LinguaFlow/backend/internal/parser/text"
)
