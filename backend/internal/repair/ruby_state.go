package repair

import (
	"encoding/json"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// TryRepairRubyAlignmentForState applies the legacy envelope repair contract,
// keeping every identifiable row for the candidate's duplicate-ID validation.
// A malformed annotation never discards independently valid annotations.
func TryRepairRubyAlignmentForState(text string, opt Options) ([]ruby.OutputEntry, []string, error) {
	if opt.Bounds != nil {
		opt.BareArrayAccept = ruby.ValidBareOutputEntries
		raw, ops, e := TryRepairEnvelope(text, "ruby_output", opt)
		if e != nil {
			return nil, ops, e
		}
		data, e := opt.Bounds.EncodeJSON(raw["ruby_output"], opt.Bounds.Snapshot().TextLimit)
		if e != nil {
			return nil, ops, e
		}
		entries, e := ruby.ParseOutputEntriesBounded(opt.Bounds, data)
		if e != nil {
			return nil, ops, e
		}
		for i := range entries {
			entries[i].Base = strings.TrimSpace(entries[i].Base)
			entries[i].Text = strings.TrimSpace(entries[i].Text)
			entries[i].Kind = strings.TrimSpace(entries[i].Kind)
		}
		return entries, ops, nil
	}
	opt.BareArrayAccept = ruby.ValidBareOutputEntries
	envelope, repaired, err := TryRepairEnvelope(text, "ruby_output", opt)
	if err != nil {
		return nil, repaired, err
	}
	data, err := json.Marshal(envelope["ruby_output"])
	if err != nil {
		return nil, repaired, err
	}
	entries, err := ruby.ParseOutputEntries(data)
	if err != nil {
		return nil, repaired, err
	}
	for i := range entries {
		entries[i].Base = strings.TrimSpace(entries[i].Base)
		entries[i].Text = strings.TrimSpace(entries[i].Text)
		entries[i].Kind = strings.TrimSpace(entries[i].Kind)
	}
	return entries, repaired, nil
}
