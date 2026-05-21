package progress

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNop_NoSideEffects(t *testing.T) {
	var r Reporter = Nop{}
	r.StageStart("x", 10)
	r.SegmentDone()
	r.StageDone()
	if err := r.Close(); err != nil {
		t.Fatalf("Nop.Close should be nil, got %v", err)
	}
}

func TestTerminal_RendersCountAndCloses(t *testing.T) {
	var buf bytes.Buffer
	r := NewTerminal(&buf)
	r.StageStart("translate", 3)
	r.SegmentDone()
	r.SegmentDone()
	r.SegmentDone()
	if err := r.Close(); err != nil {
		t.Fatalf("Close err: %v", err)
	}
	out := buf.String()
	// progressbar 输出含描述与计数；至少应包含阶段名。
	if !strings.Contains(out, "translate") {
		t.Errorf("expected stage name in output, got %q", out)
	}
}

func TestTerminal_NoTotalSkipsBar(t *testing.T) {
	var buf bytes.Buffer
	r := NewTerminal(&buf)
	r.StageStart("split", 0) // 没有段级进度
	r.SegmentDone()           // 应被忽略，不 panic
	r.StageDone()
	_ = r.Close()
	out := buf.String()
	if !strings.Contains(out, "split") {
		t.Errorf("expected stage name in output, got %q", out)
	}
}

func TestLog_EmitsByCount(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	r := NewLog(logger, 0, 5) // 仅按段数
	r.StageStart("translate", 12)
	for i := 0; i < 12; i++ {
		r.SegmentDone()
	}
	r.StageDone()
	_ = r.Close()

	out := buf.String()
	progressLines := strings.Count(out, `msg="stage progress"`)
	// StageStart 一条 + 每 5 段一条（5、10）= 3 条 progress
	if progressLines != 3 {
		t.Errorf("expected 3 progress lines, got %d in: %s", progressLines, out)
	}
	if !strings.Contains(out, `msg="stage done"`) {
		t.Errorf("expected stage done line, got %s", out)
	}
}

func TestLog_EmitsByTime(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	r := NewLog(logger, 30*time.Millisecond, 0) // 仅按时间
	r.StageStart("translate", 100)
	r.SegmentDone() // 紧贴 StageStart，不应触发
	time.Sleep(40 * time.Millisecond)
	r.SegmentDone() // 超阈值，应触发一条
	r.StageDone()
	_ = r.Close()

	out := buf.String()
	progressLines := strings.Count(out, `msg="stage progress"`)
	// StageStart 1 + 时间触发 1
	if progressLines < 2 {
		t.Errorf("expected at least 2 progress lines, got %d in: %s", progressLines, out)
	}
}

func TestLog_StageDoneWithoutTotal(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	r := NewLog(logger, 0, 5)
	r.StageStart("split", 0)
	r.StageDone()
	out := buf.String()
	if strings.Contains(out, `total=`) {
		// total=0 不该出现在 done 行
		t.Errorf("did not expect total=0 in output, got %s", out)
	}
	if !strings.Contains(out, `stage done`) {
		t.Errorf("expected stage done line, got %s", out)
	}
}

func TestLog_ConcurrentSegmentDoneSafe(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	r := NewLog(logger, 0, 100)
	r.StageStart("translate", 1000)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 125; i++ {
				r.SegmentDone()
			}
		}()
	}
	wg.Wait()
	r.StageDone()
	// 不崩溃 + 最终累计准确
	if got := r.(*logReporter).done.Load(); got != 1000 {
		t.Errorf("expected done=1000, got %d", got)
	}
}
