package cli

import (
	"path/filepath"
	"testing"
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
	if jobs[0].InputPath != input {
		t.Fatalf("InputPath = %q, want %q", jobs[0].InputPath, input)
	}
	if jobs[0].OutputPath != output {
		t.Fatalf("OutputPath = %q, want %q", jobs[0].OutputPath, output)
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
