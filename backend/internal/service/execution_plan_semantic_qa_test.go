package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	entbackend "github.com/MeowSalty/LinguaFlow/backend/internal/ent/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/schema"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
)

func TestValidateExecutionRounds_SemanticQA(t *testing.T) {
	t.Run("valid semantic_qa only", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:   10,
				Concurrency: 1,
			},
		}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("semantic_qa before translate allowed", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{
			{
				Mode:      "semantic_qa",
				BackendID: 1,
				SemanticQA: &schema.SemanticQARoundConfig{
					BatchSize:   5,
					Concurrency: 1,
				},
			},
			{
				Mode:      "translate",
				BackendID: 1,
				Translate: &schema.TranslateRoundConfig{
					PromptTemplateID: -1,
					ProfileID:        -1,
					BatchSize:        10,
					Concurrency:      1,
					FallbackShrink:   1.0,
				},
			},
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("nil semantic_qa config", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
		}})
		if !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})

	t.Run("both batch limits zero", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				Concurrency: 1,
			},
		}})
		if !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})

	t.Run("invalid segment_scope", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:    10,
				Concurrency:  1,
				SegmentScope: "invalid",
			},
		}})
		if !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})

	t.Run("invalid issue_codes", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:    10,
				Concurrency:  1,
				SegmentScope: "with_issue_codes",
				IssueCodes:   []string{"foo"},
			},
		}})
		if !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})

	t.Run("empty segment_scope allowed", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:   10,
				Concurrency: 1,
			},
		}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("with_issue_codes and valid codes", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:    10,
				Concurrency:  1,
				SegmentScope: "with_issue_codes",
				IssueCodes:   []string{"source_residual", "calque"},
			},
		}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("with_issue_codes and new semantic codes", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:    10,
				Concurrency:  1,
				SegmentScope: "with_issue_codes",
				IssueCodes:   []string{"mistranslation", "omission", "addition", "grammar", "register"},
			},
		}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("with_issue_codes mixed rule and new codes", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:    10,
				Concurrency:  1,
				SegmentScope: "with_issue_codes",
				IssueCodes:   []string{"untranslated", "mistranslation", "calque", "register"},
			},
		}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("with_issue_codes accepts deterministic checker codes", func(t *testing.T) {
		// 全部确定性 checker code（含新纳入的 11 个）均可作筛选键
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:    10,
				Concurrency:  1,
				SegmentScope: "with_issue_codes",
				IssueCodes:   []string{"punctuation_pairing", "number_mismatch", "forbidden_term", "xml_tag_mismatch"},
			},
		}})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("with_issue_codes requires codes", func(t *testing.T) {
		err := validateExecutionRounds([]schema.ExecutionRoundConfig{{
			Mode:      "semantic_qa",
			BackendID: 1,
			SemanticQA: &schema.SemanticQARoundConfig{
				BatchSize:    10,
				Concurrency:  1,
				SegmentScope: "with_issue_codes",
			},
		}})
		if !errors.Is(err, ErrExecutionPlanConfigInvalid) {
			t.Fatalf("err=%v want ErrExecutionPlanConfigInvalid", err)
		}
	})
}

func TestValidateExecutionRounds_Revise(t *testing.T) {
	validConfig := func() *schema.ReviseRoundConfig {
		return &schema.ReviseRoundConfig{
			BatchSize:   10,
			Concurrency: 1,
		}
	}

	tests := []struct {
		name    string
		config  *schema.ReviseRoundConfig
		wantErr string
	}{
		{name: "valid with default scope", config: validConfig()},
		{name: "nil config", wantErr: "revise config required"},
		{name: "with_issue_codes requires codes", config: func() *schema.ReviseRoundConfig {
			c := validConfig()
			c.SegmentScope = "with_issue_codes"
			return c
		}(), wantErr: "issue_codes must contain at least one code"},
		{name: "invalid code", config: func() *schema.ReviseRoundConfig {
			c := validConfig()
			c.SegmentScope = "with_issue_codes"
			c.IssueCodes = []string{"source_residual"}
			return c
		}(), wantErr: "contains invalid code"},
		{name: "invalid scope", config: func() *schema.ReviseRoundConfig {
			c := validConfig()
			c.SegmentScope = "all"
			return c
		}(), wantErr: "segment_scope must be"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			round := schema.ExecutionRoundConfig{Mode: "revise", BackendID: 1, Revise: tt.config}
			err := validateExecutionRounds([]schema.ExecutionRoundConfig{round})
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err=%v want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestMaterializeReviseSnapshotDefaults(t *testing.T) {
	s := &schema.ReviseRoundConfig{
		BatchSize:   5,
		Concurrency: 1,
	}
	snap := snapshotReviseRound(s)
	if snap.SegmentScope != "with_issues" {
		t.Fatalf("SegmentScope=%q want with_issues", snap.SegmentScope)
	}
	if !reflect.DeepEqual(snap.IssueCodes, qa.SemanticQACodes()) {
		t.Fatalf("IssueCodes=%v want %v", snap.IssueCodes, qa.SemanticQACodes())
	}
	codes := append([]string(nil), snap.IssueCodes...)
	s.IssueCodes = []string{"calque"}
	if !reflect.DeepEqual(snap.IssueCodes, codes) {
		t.Fatalf("snapshot IssueCodes mutated via shared slice: %v", snap.IssueCodes)
	}

	// with_issues 即使用户误填 issue_codes，也物化为完整语义白名单：
	// 规范约定 issue_codes 仅 with_issue_codes 生效，执行链无 scope 维度，
	// 保留子集会漏掉其余 pending 语义 issue。
	misconfig := &schema.ReviseRoundConfig{
		BatchSize:    5,
		Concurrency:  1,
		SegmentScope: "with_issues",
		IssueCodes:   []string{"calque"},
	}
	if snap := snapshotReviseRound(misconfig); !reflect.DeepEqual(snap.IssueCodes, qa.SemanticQACodes()) {
		t.Fatalf("with_issues IssueCodes=%v want full semantic whitelist", snap.IssueCodes)
	}

	withCodes := &schema.ReviseRoundConfig{
		BatchSize:    5,
		Concurrency:  1,
		SegmentScope: "with_issue_codes",
		IssueCodes:   []string{"calque", "grammar"},
	}
	withCodesSnap := snapshotReviseRound(withCodes)
	if !reflect.DeepEqual(withCodesSnap.IssueCodes, withCodes.IssueCodes) {
		t.Fatalf("IssueCodes=%v want %v", withCodesSnap.IssueCodes, withCodes.IssueCodes)
	}
}

func TestValidateAndSnapshotRevise(t *testing.T) {
	client := testClient(t)
	user, err := client.User.Create().
		SetUsername("revise-user").
		SetPasswordHash("hash").
		SetEmail("revise@test.com").
		Save(context.Background())
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	users := NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client)))
	backends := NewBackendService(client, users, nil)
	backendRow, err := client.Backend.Create().
		SetName("revise-backend").
		SetBackendType(entbackend.BackendType("openai")).
		SetScope("user").
		SetOwnerUserID(user.ID).
		SetOptions(map[string]any{}).
		Save(context.Background())
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}
	jobs := &JobService{client: client, backends: backends}
	plan := &ent.ExecutionPlanTemplate{
		ID:   1,
		Name: "revise-plan",
		Rounds: []schema.ExecutionRoundConfig{{
			Mode:      "revise",
			BackendID: backendRow.ID,
			Revise: &schema.ReviseRoundConfig{
				BatchSize:   10,
				Concurrency: 1,
			},
		}},
	}
	snap, err := jobs.validateAndSnapshotWith(context.Background(), plan, "", func(id int) error {
		if id != backendRow.ID {
			t.Fatalf("backend id=%d want %d", id, backendRow.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(snap.Rounds) != 1 || snap.Rounds[0].Revise == nil {
		t.Fatalf("snapshot rounds=%+v", snap.Rounds)
	}
	if !reflect.DeepEqual(snap.Rounds[0].Revise.IssueCodes, qa.SemanticQACodes()) {
		t.Fatalf("snapshot IssueCodes=%v want %v", snap.Rounds[0].Revise.IssueCodes, qa.SemanticQACodes())
	}

	checkSnapshotError := func(name string, cfg *schema.ReviseRoundConfig, want string) {
		t.Helper()
		plan.Rounds[0].Revise = cfg
		_, err := jobs.validateAndSnapshotWith(context.Background(), plan, "", func(int) error { return nil })
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err=%v want substring %q", name, err, want)
		}
	}
	checkSnapshotError("with_issue_codes requires codes", &schema.ReviseRoundConfig{
		BatchSize:    10,
		Concurrency:  1,
		SegmentScope: "with_issue_codes",
	}, "rounds[0] revise issue_codes is empty for with_issue_codes")
	checkSnapshotError("invalid semantic code", &schema.ReviseRoundConfig{
		BatchSize:    10,
		Concurrency:  1,
		SegmentScope: "with_issue_codes",
		IssueCodes:   []string{"source_residual"},
	}, "rounds[0] revise issue_codes contains invalid code: source_residual")
	checkSnapshotError("invalid scope", &schema.ReviseRoundConfig{
		BatchSize:    10,
		Concurrency:  1,
		SegmentScope: "all",
	}, "rounds[0] revise segment_scope invalid: all")
	checkSnapshotError("nil config", nil, "rounds[0] revise config is nil")
}

func TestMaterializeSemanticQASnapshotDefaults(t *testing.T) {
	t.Run("empty scope materializes to all", func(t *testing.T) {
		s := &schema.SemanticQARoundConfig{
			BatchSize:   5,
			Concurrency: 1,
			IssueCodes:  []string{"source_residual"},
		}
		snap := snapshotSemanticQARound(s)
		if snap.SegmentScope != "all" {
			t.Fatalf("SegmentScope=%q want all", snap.SegmentScope)
		}
		if !reflect.DeepEqual(snap.IssueCodes, []string{"source_residual"}) {
			t.Fatalf("IssueCodes=%v", snap.IssueCodes)
		}
		// deep copy: mutating source must not affect snapshot
		s.IssueCodes[0] = "calque"
		if snap.IssueCodes[0] != "source_residual" {
			t.Fatalf("snapshot IssueCodes mutated via shared slice: %v", snap.IssueCodes)
		}
	})

	t.Run("with_issue_codes preserved", func(t *testing.T) {
		s := &schema.SemanticQARoundConfig{
			BatchSize:    5,
			Concurrency:  1,
			SegmentScope: "with_issue_codes",
			IssueCodes:   []string{"source_residual", "length_ratio"},
		}
		snap := snapshotSemanticQARound(s)
		if snap.SegmentScope != "with_issue_codes" {
			t.Fatalf("SegmentScope=%q", snap.SegmentScope)
		}
		if !reflect.DeepEqual(snap.IssueCodes, []string{"source_residual", "length_ratio"}) {
			t.Fatalf("IssueCodes=%v", snap.IssueCodes)
		}
	})
}
