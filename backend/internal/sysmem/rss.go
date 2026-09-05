// Package sysmem 提供进程级内存观测与准入控制：
//
//   - ReadRSS：读取当前进程的常驻内存集大小（RSS），按平台适配
//     （Linux /proc、Windows 工作集、其他平台近似值）；
//   - Gate：基于 RSS 的双水位准入闸门，内存吃紧时暂停新资源准入（只出不进）。
package sysmem

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// ReadRSS 返回当前进程的常驻内存集大小（RSS），单位字节。
//
// 各平台实现：
//   - Linux：解析 /proc/self/status 的 VmRSS 行（kB → 字节），
//     status 缺失或解析失败时回退到 /proc/self/statm；
//   - Windows：GetProcessMemoryInfo 的 WorkingSetSize（工作集大小）；
//   - 其他平台（如 macOS）：以 runtime HeapAlloc*2 近似（见 rss_other.go）。
func ReadRSS() (uint64, error) {
	return readRSS()
}

// procStatusVmRSSPrefix 是 /proc/self/status 中 VmRSS 行的前缀。
// 按行前缀匹配而非子串匹配，避免误匹配包含 "VmRSS" 字样的其他行。
var procStatusVmRSSPrefix = []byte("VmRSS:")

// parseProcStatusRSS 从 /proc/self/status 的内容中解析 VmRSS 行，
// 返回常驻内存字节数（内核固定以 kB 为单位，1 kB = 1024 字节）。
// 未找到 VmRSS 行、数值非法或单位异常时返回错误。
// 纯解析逻辑，不依赖平台，便于跨平台单元测试。
func parseProcStatusRSS(data []byte) (uint64, error) {
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, procStatusVmRSSPrefix) {
			continue
		}
		fields := bytes.Fields(line[len(procStatusVmRSSPrefix):])
		if len(fields) == 0 {
			return 0, errors.New("sysmem: VmRSS 行缺少数值")
		}
		kb, err := strconv.ParseUint(string(fields[0]), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("sysmem: 解析 VmRSS 数值 %q: %w", fields[0], err)
		}
		// 内核固定输出 "kB" 单位；若出现其他单位说明格式变更，宁可报错。
		// 仅有一个字段时予以容错（无单位）。
		if len(fields) >= 2 && string(fields[1]) != "kB" {
			return 0, fmt.Errorf("sysmem: VmRSS 单位异常: %q", fields[1])
		}
		if kb > math.MaxUint64/1024 {
			return 0, fmt.Errorf("sysmem: VmRSS 数值溢出: %d kB", kb)
		}
		return kb * 1024, nil
	}
	return 0, errors.New("sysmem: /proc/self/status 中未找到 VmRSS 行")
}

// parseStatmRSS 从 /proc/self/statm 的内容解析常驻内存字节数。
// statm 首行格式为 "size resident shared text lib data dt"，单位为页，
// resident（第二个字段）即常驻页数，乘以页大小得到字节数。
// pageSize 由调用方传入（通常为 os.Getpagesize()），便于跨平台测试。
func parseStatmRSS(data []byte, pageSize int) (uint64, error) {
	if pageSize <= 0 {
		return 0, fmt.Errorf("sysmem: 非法页大小: %d", pageSize)
	}
	fields := bytes.Fields(data)
	if len(fields) < 2 {
		return 0, fmt.Errorf("sysmem: /proc/self/statm 字段不足: %q", string(data))
	}
	pages, err := strconv.ParseUint(string(fields[1]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("sysmem: 解析 statm resident 字段 %q: %w", fields[1], err)
	}
	if pages > math.MaxUint64/uint64(pageSize) {
		return 0, fmt.Errorf("sysmem: statm resident 数值溢出: %d 页", pages)
	}
	return pages * uint64(pageSize), nil
}
