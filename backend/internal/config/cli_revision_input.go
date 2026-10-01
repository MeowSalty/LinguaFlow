package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// CLIRevisionInput is user-supplied translation data, not configuration or a
// saved execution snapshot. Text is always literal, including ${...} sequences.
type CLIRevisionInput struct {
	SchemaVersion int                  `yaml:"schema_version"`
	Segments      []CLIRevisionSegment `yaml:"segments"`
}

type CLIRevisionSegment struct {
	Index  *int               `yaml:"index"`
	Source string             `yaml:"source"`
	Target string             `yaml:"target"`
	Issues []CLIRevisionIssue `yaml:"issues"`
}

type CLIRevisionIssue struct {
	Code    string `yaml:"code"`
	Message string `yaml:"message"`
	Snippet string `yaml:"snippet"`
}

func LoadCLIRevisionInput(path string) (*CLIRevisionInput, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("--revision-input must not be empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cannot read revision input")
	}
	node, err := parseTranslationYAML(data)
	if err != nil {
		return nil, fmt.Errorf("revision input: %w", err)
	}
	var input CLIRevisionInput
	if err := strictTranslationDecode(node, &input, "revision input"); err != nil {
		return nil, err
	}
	if input.SchemaVersion != 1 {
		return nil, errors.New("revision input requires schema_version: 1")
	}
	if len(input.Segments) == 0 {
		return nil, errors.New("revision input requires at least one reviewed segment")
	}
	seen := make(map[int]bool, len(input.Segments))
	for i, segment := range input.Segments {
		if segment.Index == nil || *segment.Index < 0 || seen[*segment.Index] {
			return nil, fmt.Errorf("revision input segments[%d] requires a unique nonnegative index", i)
		}
		seen[*segment.Index] = true
		if strings.TrimSpace(segment.Source) == "" || strings.TrimSpace(segment.Target) == "" || len(segment.Issues) == 0 {
			return nil, fmt.Errorf("revision input segments[%d] requires source, existing target and issues", i)
		}
		for _, issue := range segment.Issues {
			if !qa.IsSemanticQACode(issue.Code) || strings.TrimSpace(issue.Message) == "" {
				return nil, fmt.Errorf("revision input segments[%d] requires supported semantic issue codes and nonempty messages", i)
			}
		}
	}
	return &input, nil
}
