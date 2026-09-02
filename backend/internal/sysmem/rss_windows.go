//go:build windows

package sysmem

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// processMemoryCounters 对应 Win32 的 PROCESS_MEMORY_COUNTERS 结构（psapi.h），
// 字段布局与平台字长一致（uintptr = SIZE_T），本实现仅使用 WorkingSetSize。
type processMemoryCounters struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
}

var (
	modpsapi                 = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo = modpsapi.NewProc("GetProcessMemoryInfo")
)

// readRSS 返回当前进程的工作集大小（WorkingSetSize），即 Windows 语义下的 RSS。
//
// 说明：当前依赖的 golang.org/x/sys v0.46.0 未导出 GetProcessMemoryInfo，
// 故按 psapi 原型经 LazySystemDLL 自行声明，不引入新的第三方依赖。
func readRSS() (uint64, error) {
	h, err := windows.GetCurrentProcess()
	if err != nil {
		return 0, fmt.Errorf("sysmem: 获取当前进程句柄: %w", err)
	}
	var pmc processMemoryCounters
	pmc.CB = uint32(unsafe.Sizeof(pmc))
	r1, _, callErr := procGetProcessMemoryInfo.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&pmc)),
		uintptr(pmc.CB),
	)
	if r1 == 0 {
		if callErr != nil {
			return 0, fmt.Errorf("sysmem: GetProcessMemoryInfo: %w", callErr)
		}
		return 0, errors.New("sysmem: GetProcessMemoryInfo 失败")
	}
	return uint64(pmc.WorkingSetSize), nil
}
