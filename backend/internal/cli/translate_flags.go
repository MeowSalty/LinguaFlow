package cli

import (
	"errors"
	"path/filepath"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
)

func applyTranslateFlags(cfg *config.CLIConfig, opts translateOptions) error {
	changed := func(name string) bool { return opts.changed[name] }
	if changed("from") {
		if opts.from == "" {
			return errors.New("--from must not be empty")
		}
		cfg.SourceLang = opts.from
	}
	if changed("to") {
		if opts.to == "" {
			return errors.New("--to must not be empty")
		}
		cfg.TargetLang = opts.to
	}
	if changed("glossary-path") {
		if opts.glossaryPath == "" {
			return errors.New("--glossary-path must not be empty")
		}
		path, err := filepath.Abs(opts.glossaryPath)
		if err != nil {
			return err
		}
		cfg.Glossary.Path = path
		cfg.Glossary.Enabled = true
	}
	if changed("profile") {
		if _, ok := cfg.TranslationProfiles[opts.profile]; !ok || opts.profile == "" {
			return errors.New("--profile must name an existing profile")
		}
		cfg.Execution.Profile = opts.profile
	}
	if changed("prompt") {
		if _, ok := cfg.PromptTemplates[opts.prompt]; !ok || opts.prompt == "" {
			return errors.New("--prompt must name an existing translation template")
		}
		for i := range cfg.Execution.Rounds {
			r := &cfg.Execution.Rounds[i]
			if r.Translate != nil {
				r.Translate.Prompt = opts.prompt
			}
		}
	}
	if changed("bootstrap") {
		if opts.bootstrapMode != "off" && opts.bootstrapMode != "pre" && opts.bootstrapMode != "inline" {
			return errors.New("--bootstrap must be off, pre or inline")
		}
		for i := range cfg.Execution.Rounds {
			r := &cfg.Execution.Rounds[i]
			if r.Mode != "translate" || r.Translate == nil {
				continue
			}
			inline := execution.DefaultInlineTermExtraction()
			if r.Translate.InlineTermExtraction != nil {
				inline = *r.Translate.InlineTermExtraction
			}
			inline.Enabled = opts.bootstrapMode == "inline"
			r.Translate.InlineTermExtraction = &inline
		}
		var extract, other []config.CLIConfigRound
		for _, r := range cfg.Execution.Rounds {
			if r.Mode == "extract" {
				extract = append(extract, r)
			} else {
				other = append(other, r)
			}
		}
		if opts.bootstrapMode == "pre" {
			if len(extract) == 0 {
				backend := ""
				for _, r := range other {
					if r.Mode == "translate" {
						backend = r.Backend
						break
					}
				}
				extract = []config.CLIConfigRound{{Mode: "extract", Backend: backend, Extract: &config.CLIConfigExtractRound{BatchSize: 20, Concurrency: 2, MaxTermsPer1000Chars: 25, MinSourceLen: 2, Retry: config.RetryConfig{MaxAttempts: 3, BackoffMs: 2000, Jitter: true}}}}
			}
			cfg.Execution.Rounds = append(extract, other...)
		} else {
			cfg.Execution.Rounds = other
		}
		if opts.bootstrapMode != "off" {
			cfg.Glossary.Enabled = true
		}
	}
	return config.ValidateCLIConfig(cfg)
}
