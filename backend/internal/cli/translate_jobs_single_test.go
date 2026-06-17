package cli

import (
	"path/filepath"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
)

func TestBuildTranslateJobsSingleFile(t *testing.T) {
	tmp := t.TempDir()
	input := filepath.Join(tmp, "input.md")
	output := filepath.Join(tmp, "output.md")
	writeTestFile(t, input)

	jobs, report, err := buildTranslateJobs([]string{input}, output)
	if err != nil {
		t.Fatalf("buildTranslateJobs() error = %v", err)
	}
	if len(report.Ignored) != 0 {
		t.Fatalf("ignored = %d, want 0", len(report.Ignored))
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(jobs))
	}
	reader, ok := jobs[0].Source.(*engine.FileReader)
	if !ok {
		t.Fatalf("Source type = %T, want *engine.FileReader", jobs[0].Source)
	}
	if reader.Path != input {
		t.Fatalf("Source.Path = %q, want %q", reader.Path, input)
	}
	writer, ok := jobs[0].Sink.(*engine.FileWriter)
	if !ok {
		t.Fatalf("Sink type = %T, want *engine.FileWriter", jobs[0].Sink)
	}
	if writer.Path != output {
		t.Fatalf("Sink.Path = %q, want %q", writer.Path, output)
	}
}

func TestBuildTranslateJobsMultiInputsRequireOutputDir(t *testing.T) {
	tmp := t.TempDir()
	inputA := filepath.Join(tmp, "a.md")
	inputB := filepath.Join(tmp, "b.txt")
	outputFile := filepath.Join(tmp, "output.md")
	writeTestFile(t, inputA)
	writeTestFile(t, inputB)
	writeTestFile(t, outputFile)

	_, _, err := buildTranslateJobs([]string{inputA, inputB}, outputFile)
	if err == nil {
		t.Fatal("buildTranslateJobs() error = nil, want error")
	}
}
