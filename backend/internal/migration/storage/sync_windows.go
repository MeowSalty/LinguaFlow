//go:build windows

package storagemigrate

// Windows 没有目录 fsync。当检查点文件更新丢失时，
// 数据库操作记录是权威依据；resume 会在写入前对其做核对。
func syncManifestDir(string) error { return nil }
