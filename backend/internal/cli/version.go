package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// Version 由构建时通过 -ldflags 注入；默认值为 dev。
var (
	Version = "dev"
	Commit  = "unknown"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "打印版本信息",
		Run: func(_ *cobra.Command, _ []string) {
			commit := Commit
			if commit == "unknown" {
				if info, ok := debug.ReadBuildInfo(); ok {
					for _, s := range info.Settings {
						if s.Key == "vcs.revision" && s.Value != "" {
							commit = s.Value
							break
						}
					}
				}
			}
			fmt.Printf("linguaflow %s (commit %s) %s/%s %s\n",
				Version, commit, runtime.GOOS, runtime.GOARCH, runtime.Version())
		},
	}
}
