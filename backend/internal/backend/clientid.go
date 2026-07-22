package backend

import "github.com/MeowSalty/LinguaFlow/backend/internal/version"

// ClientUserAgent 返回 LLM 出站请求的 User-Agent。
func ClientUserAgent() string { return version.UserAgent() }

// ClientName 返回 LLM 出站请求的 X-Client-Name。
func ClientName() string { return version.ClientName() }

// ClientVersion 返回 LLM 出站请求的 X-Client-Version。
func ClientVersion() string { return version.ClientVersion() }
