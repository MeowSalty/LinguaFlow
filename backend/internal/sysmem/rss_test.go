package sysmem

import (
	"os"
	"testing"
)

// sampleStatus 模拟 /proc/self/status 的典型内容（节选）。
const sampleStatus = `Name:	linguaflow
Umask:	0022
State:	R (running)
Tgid:	12345
Ngid:	0
Pid:	12345
PPid:	1
TracerPid:	0
Uid:	1000	1000	1000	1000
Gid:	1000	1000	1000	1000
FDSize:	256
Groups:	20 1000
VmPeak:	  2048000 kB
VmSize:	  1024000 kB
VmLck:	       0 kB
VmPin:	       0 kB
VmHWM:	  512000 kB
VmRSS:	  655360 kB
RssAnon:	  327680 kB
RssFile:	  327680 kB
RssShmem:	       0 kB
VmData:	  983040 kB
VmStk:	     132 kB
VmExe:	  284532 kB
VmLib:	   12288 kB
VmPTE:	     896 kB
VmSwap:	       0 kB
HugetlbPages:	       0 kB
CoreDumping:	1
Threads:	42
SigPnd:	0000000000000000
voluntary_ctxt_switches:	120
nonvoluntary_ctxt_switches:	5
`

func TestParseProcStatusRSS(t *testing.T) {
	tests := []struct {
		name    string
		data    string
		want    uint64
		wantErr bool
	}{
		{
			name: "典型样例-VmRSS 位于中间",
			data: sampleStatus,
			want: 655360 * 1024,
		},
		{
			name: "VmRSS 为 0",
			data: "Name:\tfoo\nVmRSS:\t       0 kB\nThreads:\t1\n",
			want: 0,
		},
		{
			name:    "缺少 VmRSS 行",
			data:    "Name:\tfoo\nVmSize:\t  1024 kB\n",
			wantErr: true,
		},
		{
			name:    "数值非法",
			data:    "VmRSS:\tabc kB\n",
			wantErr: true,
		},
		{
			name:    "空数据",
			data:    "",
			wantErr: true,
		},
		{
			name:    "单位异常",
			data:    "VmRSS:\t1234 MB\n",
			wantErr: true,
		},
		{
			name: "无单位时容错",
			data: "VmRSS:\t1234\n",
			want: 1234 * 1024,
		},
		{
			name: "CRLF 行尾",
			data: "Name:\tfoo\r\nVmRSS:\t2048 kB\r\n",
			want: 2048 * 1024,
		},
		{
			name: "行前缀匹配-不会误匹配含 VmRSS 字样的其他行",
			data: "FakeVmRSS:\t9999 kB\nVmRSS:\t100 kB\n",
			want: 100 * 1024,
		},
		{
			name:    "VmRSS 行缺少数值",
			data:    "VmRSS:\n",
			wantErr: true,
		},
		{
			name: "无尾随换行",
			data: "VmRSS:\t4096 kB",
			want: 4096 * 1024,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseProcStatusRSS([]byte(tt.data))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseProcStatusRSS(%q) 期望报错，实际得到 %d", tt.data, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseProcStatusRSS(%q): %v", tt.data, err)
			}
			if got != tt.want {
				t.Errorf("parseProcStatusRSS(%q) = %d, 期望 %d", tt.data, got, tt.want)
			}
		})
	}
}

func TestParseStatmRSS(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		pageSize int
		want     uint64
		wantErr  bool
	}{
		{
			name:     "典型七字段",
			data:     "1024 256 128 0 0 512 0",
			pageSize: 4096,
			want:     256 * 4096,
		},
		{
			name:     "按传入页大小换算",
			data:     "10 3 1 0 0 2 0",
			pageSize: 16384,
			want:     3 * 16384,
		},
		{
			name:     "resident 为 0",
			data:     "1 0 0 0 0 0 0",
			pageSize: 4096,
			want:     0,
		},
		{
			name:     "字段不足",
			data:     "123",
			pageSize: 4096,
			wantErr:  true,
		},
		{
			name:     "空数据",
			data:     "",
			pageSize: 4096,
			wantErr:  true,
		},
		{
			name:     "resident 非数字",
			data:     "100 abc 50 0 0 0 0",
			pageSize: 4096,
			wantErr:  true,
		},
		{
			name:     "页大小非法",
			data:     "1 1 0 0 0 0 0",
			pageSize: 0,
			wantErr:  true,
		},
		{
			name:     "页大小为负",
			data:     "1 1 0 0 0 0 0",
			pageSize: -4096,
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStatmRSS([]byte(tt.data), tt.pageSize)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseStatmRSS(%q, %d) 期望报错，实际得到 %d", tt.data, tt.pageSize, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseStatmRSS(%q, %d): %v", tt.data, tt.pageSize, err)
			}
			if got != tt.want {
				t.Errorf("parseStatmRSS(%q, %d) = %d, 期望 %d", tt.data, tt.pageSize, got, tt.want)
			}
		})
	}
}

// TestReadRSS 冒烟测试：在所有支持平台上 ReadRSS 应成功且返回非零值
// （测试进程运行中 RSS / 工作集 / 堆近似值不可能为 0）。
func TestReadRSS(t *testing.T) {
	rss, err := ReadRSS()
	if err != nil {
		t.Fatalf("ReadRSS: %v", err)
	}
	if rss == 0 {
		t.Error("ReadRSS 返回 0，预期为非零正值")
	}
	t.Logf("当前进程 RSS = %d 字节", rss)

	// 第二次调用应继续成功（无状态污染）。
	if _, err := ReadRSS(); err != nil {
		t.Errorf("第二次 ReadRSS: %v", err)
	}
}

// TestReadRSSWithRealProc 在 Linux 上验证真实 /proc 数据可被解析
// （其他平台跳过）。
func TestReadRSSWithRealProc(t *testing.T) {
	if _, err := os.Stat("/proc/self/status"); err != nil {
		t.Skip("非 Linux 环境，跳过")
	}
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatalf("读取 /proc/self/status: %v", err)
	}
	rss, err := parseProcStatusRSS(data)
	if err != nil {
		t.Fatalf("解析真实 /proc/self/status: %v", err)
	}
	if rss == 0 {
		t.Error("真实 /proc/self/status 解析结果为 0")
	}
}
