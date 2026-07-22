package cli

import (
	"fmt"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/MeowSalty/LinguaFlow/backend/internal/version"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "打印版本信息",
		Run: func(_ *cobra.Command, _ []string) {
			commit := version.Commit
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
				version.ResolvedVersion(), commit, runtime.GOOS, runtime.GOARCH, runtime.Version())
		},
	}
}
