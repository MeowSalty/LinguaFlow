//go:build !windows && !linux && !darwin

package operation

func renameNoReplace(string, string) error {
	return Fail(UnsupportedTarget, "atomic no-replace publication is unavailable on this platform")
}
