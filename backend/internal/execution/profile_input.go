package execution

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

// ProfileInput preserves member presence, including false, zero and empty
// arrays. The raw representation is consumed only at input boundaries.
type ProfileInput json.RawMessage

func DecodeProfileJSON(input []byte, base ProfileSpec) (ProfileSpec, error) {
	if err := checkInputJSON(json.NewDecoder(bytes.NewReader(input))); err != nil {
		return ProfileSpec{}, err
	}
	// Clone before decoding: a PATCH must not share arrays with its input state.
	copyData, err := json.Marshal(base)
	if err != nil {
		return ProfileSpec{}, err
	}
	var out ProfileSpec
	if err := json.Unmarshal(copyData, &out); err != nil {
		return ProfileSpec{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return ProfileSpec{}, errors.New("invalid profile field or type")
	}
	if err := ValidateProfile(out); err != nil {
		return ProfileSpec{}, err
	}
	return out, nil
}

func checkInputJSON(dec *json.Decoder) error {
	var visit func() error
	visit = func() error {
		t, err := dec.Token()
		if err != nil {
			return errors.New("invalid profile JSON")
		}
		if t == nil {
			return errors.New("profile null values are not supported")
		}
		d, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch d {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				t, err := dec.Token()
				if err != nil {
					return err
				}
				k, ok := t.(string)
				if !ok || seen[k] {
					return errors.New("duplicate profile field")
				}
				seen[k] = true
				if err := visit(); err != nil {
					return err
				}
			}
		case '[':
			for dec.More() {
				if err := visit(); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid profile JSON")
		}
		_, err = dec.Token()
		return err
	}
	if err := visit(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("profile must contain one JSON object")
	}
	return nil
}

func ValidateProfile(p ProfileSpec) error {
	if p.SchemaVersion != SchemaVersion {
		return errors.New("unsupported or missing profile schema version")
	}
	allowedChecks := map[string]bool{qa.CodeDuplicateSourceDivergence: true}
	for _, name := range qa.AllCheckerNames() {
		allowedChecks[name] = true
	}
	for _, name := range p.QA.Checks {
		if !allowedChecks[name] {
			return errors.New("unsupported QA checker name")
		}
	}
	if p.Context.Before < 0 || p.Context.After < 0 || p.Context.MaxChars < 0 {
		return errors.New("context window values cannot be negative")
	}
	for _, kind := range p.Ruby.PreserveKinds {
		if kind != "phonetic" && kind != "semantic" && kind != "creative" {
			return errors.New("invalid ruby preserve kind")
		}
	}
	allowedRules := map[string]bool{"code": true, "link": true, "placeholder": true, "xml": true}
	for _, rule := range p.Protect.Rules {
		if !allowedRules[rule] {
			return errors.New("unsupported protection rule")
		}
	}
	b := p.Glossary.Bootstrap
	if b.MaxTermsPer1000Chars < 0 || b.MinSourceLen < 0 {
		return errors.New("invalid glossary bootstrap limits")
	}
	if b.Enabled && b.InlineConflictStrategy != "off" && b.InlineConflictStrategy != "rewrite-local" {
		return errors.New("invalid glossary conflict strategy")
	}
	if p.QA.Enabled {
		if p.QA.LengthRatioMin < 0 || p.QA.LengthRatioMax < 0 {
			return errors.New("QA ratios cannot be negative")
		}
		if p.QA.LengthMethod != "" && p.QA.LengthMethod != "char_weight" && p.QA.LengthMethod != "word_count" {
			return errors.New("invalid QA length method")
		}
		if p.QA.LengthRatioMin > 0 && p.QA.LengthRatioMax > 0 && p.QA.LengthRatioMin > p.QA.LengthRatioMax {
			return fmt.Errorf("QA minimum ratio must not exceed maximum")
		}
	}
	return nil
}
