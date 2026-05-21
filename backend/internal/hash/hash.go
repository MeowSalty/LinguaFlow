// Package hash 提供段落级稳定哈希，用于 Segment ID 与增量翻译跳过判断。
package hash

import (
	"crypto/sha256"
	"encoding/hex"
)

// Short 返回 12 字符的十六进制哈希（48 bit），冲突概率对单文档段落数足够低。
func Short(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

// Full 返回完整 64 字符 SHA-256 哈希。
func Full(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
