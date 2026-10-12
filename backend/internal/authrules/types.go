// Package authrules evaluates bounded, typed authentication policies without I/O.
package authrules

import (
	"encoding/json"
	"time"
)

type Field struct {
	FieldID        string   `json:"field_id"`
	Label          string   `json:"label"`
	Type           string   `json:"type"`
	Source         string   `json:"source"`
	Pointer        string   `json:"pointer"`
	RequiredScopes []string `json:"required_scopes,omitempty"`
	Optional       bool     `json:"optional"`
	UserEditable   bool     `json:"user_editable"`
	DerivedFrom    []string `json:"derived_from,omitempty"`
}
type RuleSet struct {
	Mode       string      `json:"mode"`
	Match      string      `json:"match,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
}
type Condition struct {
	FieldID  string          `json:"field_id"`
	Operator string          `json:"operator"`
	Value    json.RawMessage `json:"value"`
}
type Attribute struct {
	Value       json.RawMessage `json:"value,omitempty"`
	Status      string          `json:"status"`
	Reason      string          `json:"reason,omitempty"`
	Fingerprint string          `json:"fingerprint"`
}
type Evidence struct {
	Issuer              string               `json:"issuer"`
	Subject             string               `json:"subject"`
	SourceFingerprint   string               `json:"source_fingerprint"`
	LoginPolicyRevision int64                `json:"login_policy_revision"`
	ObservedAt          time.Time            `json:"observed_at"`
	ExpiresAt           time.Time            `json:"expires_at"`
	Attributes          map[string]Attribute `json:"attributes"`
}
type ConditionResult struct {
	FieldID  string `json:"field_id"`
	Operator string `json:"operator"`
	Status   string `json:"status"`
	Reason   string `json:"reason,omitempty"`
}
type Result struct {
	Status     string            `json:"status"`
	Conditions []ConditionResult `json:"conditions"`
	Retryable  bool              `json:"retryable"`
}
type Issue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func Unrestricted() RuleSet { return RuleSet{Mode: "unrestricted"} }
