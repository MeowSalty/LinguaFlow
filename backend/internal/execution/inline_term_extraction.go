package execution

import (
	"errors"
	"math"
)

// InlineTermExtractionConfig controls term extraction in a single translate
// round's model response. A nil configuration disables extraction without
// disabling the use of existing glossary entries.
type InlineTermExtractionConfig struct {
	Enabled              bool    `json:"enabled" yaml:"enabled"`
	MaxTermsPer1000Words float64 `json:"max_terms_per_1000_words" yaml:"max_terms_per_1000_words"`
	MinSourceLen         int     `json:"min_source_len" yaml:"min_source_len"`
	ConflictStrategy     string  `json:"conflict_strategy" yaml:"conflict_strategy"`
}

// DefaultInlineTermExtraction supplies input defaults. Saved execution
// snapshots must already contain these values and are never defaulted on resume.
func DefaultInlineTermExtraction() InlineTermExtractionConfig {
	return InlineTermExtractionConfig{
		Enabled:              false,
		MaxTermsPer1000Words: 3,
		MinSourceLen:         2,
		ConflictStrategy:     "rewrite-local",
	}
}

func ValidateInlineTermExtraction(c *InlineTermExtractionConfig) error {
	if c == nil {
		return nil
	}
	if c.MaxTermsPer1000Words <= 0 || math.IsNaN(c.MaxTermsPer1000Words) || math.IsInf(c.MaxTermsPer1000Words, 0) {
		return errors.New("inline_term_extraction.max_terms_per_1000_words must be finite and positive")
	}
	if c.MinSourceLen < 1 {
		return errors.New("inline_term_extraction.min_source_len must be >= 1")
	}
	if c.ConflictStrategy != "off" && c.ConflictStrategy != "rewrite-local" {
		return errors.New("inline_term_extraction.conflict_strategy must be off or rewrite-local")
	}
	return nil
}
