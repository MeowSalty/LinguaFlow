// Package templates 加载并提供内置模板。
// 从嵌入的 default/ 目录读取 linguaflow.yaml、prompts/、profiles/。
package templates

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed default
var builtinFS embed.FS

// EmbeddedFS 返回嵌入的 default/ 文件系统。
// 用于 DefaultCLIConfigFromBuiltins 从嵌入 FS 解析 file 引用。
func EmbeddedFS() embed.FS {
	return builtinFS
}

// DefaultConfigYAML 返回带注释的默认 CLIConfig YAML 模板字节。
// 从嵌入 FS 读取，避免与 builtinFS 的双重嵌入。
func DefaultConfigYAML() []byte {
	data, err := fs.ReadFile(builtinFS, "default/linguaflow.yaml")
	if err != nil {
		panic(fmt.Sprintf("embedded linguaflow.yaml not found: %v", err))
	}
	return data
}

// EmbeddedPromptTemplate 返回嵌入的默认提示词模板内容。
func EmbeddedPromptTemplate() string {
	data, err := fs.ReadFile(builtinFS, "default/prompts/default.tmpl")
	if err != nil {
		panic(fmt.Sprintf("embedded prompts/default.tmpl not found: %v", err))
	}
	return strings.TrimRight(string(data), "\n")
}

// EmbeddedProfileConfig 返回嵌入的默认翻译策略配置字节。
func EmbeddedProfileConfig() []byte {
	data, err := fs.ReadFile(builtinFS, "default/profiles/default.yaml")
	if err != nil {
		panic(fmt.Sprintf("embedded profiles/default.yaml not found: %v", err))
	}
	return data
}
