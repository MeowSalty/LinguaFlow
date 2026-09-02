//go:build !linux && !windows

package sysmem

import "runtime"

// readRSS 在没有简单纯标准库 RSS API 的平台（如 macOS）上，
// 以 HeapAlloc*2 近似估算常驻内存。
//
// 这是刻意的近似：macOS 读取进程 RSS 需要调用 Mach 接口（如 task_info），
// 纯标准库无法完成，而为此引入 cgo 或第三方库并不值得——该读数仅用于
// Gate 的熔断启发式，2 倍堆大小的粗略估计已足以覆盖 Go 运行时在堆外的
// 常见开销（goroutine 栈、运行时结构、元数据）。若需要精确值，请为该
// 平台补充专用实现。
//
// 注意：runtime.ReadMemStats 会带来短暂 STW 暂停，调用频率由 Gate 的
// 调用方通过 Allow() 控制，本包不会后台轮询。
func readRSS() (uint64, error) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc * 2, nil
}
