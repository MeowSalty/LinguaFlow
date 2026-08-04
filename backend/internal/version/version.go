// Package version 集中持有可被 backend 与 cli 共用的版本变量。
// 由构建时 -ldflags 注入；开发态回退到 vcs.revision。
package version

import "runtime/debug"

// Version / Commit 由构建时通过 -ldflags 注入；默认值为 dev / unknown。
var (
	Version = "dev"
	Commit  = "unknown"
)

// ResolvedVersion 在 ldflags 未注入时回退到 vcs.revision，
// 保证开发态 / go run 也有非写死值。
func ResolvedVersion() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				rev := s.Value
				if len(rev) > 12 {
					rev = rev[:12]
				}
				return "git-" + rev
			}
		}
	}
	return Version
}

// UserAgent 返回出站 HTTP User-Agent 值。
func UserAgent() string { return "LinguaFlow/" + ResolvedVersion() }

// ClientName 返回 X-Client-Name 头值。
func ClientName() string { return "linguaflow" }

// ClientVersion 返回 X-Client-Version 头值。
func ClientVersion() string { return ResolvedVersion() }
