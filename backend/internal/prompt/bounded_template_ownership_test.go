package prompt

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
)

func TestBoundedTemplateRepeatedRenderRetiresPrivateBacking(t *testing.T) {
	b, err := backend.NewBuildBounds(512, 512, 512)
	if err != nil {
		t.Fatal(err)
	}
	input, err := b.OwnAllocation(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	inputBacking := make([]byte, 64)
	inputBacking[0] = 7
	renderer, err := NewRenderer(`{{define "part"}}alpha{{end}}{{template "part" .}} beta`)
	if err != nil {
		t.Fatal(err)
	}
	var outputs []string
	var owners []*backend.OwnedAllocation
	for i := 0; i < 8; i++ {
		text, err := renderSystemBounded(b, renderer.system, nil, 512)
		if err != nil || text != "alpha beta" {
			t.Fatalf("render %d: text=%q snapshot=%+v err=%v", i, text, b.Snapshot(), err)
		}
		outputs = append(outputs, text)
		owner, err := b.RetainedAllocation(int64(len(text)))
		if err != nil {
			t.Fatal(err)
		}
		owners = append(owners, owner)
		if got := b.Snapshot().LiveBytes; got != 64+int64(len("alpha beta")*(i+1)) {
			t.Fatalf("retired template kept capacity: live=%d snapshot=%+v", got, b.Snapshot())
		}
		if inputBacking[0] != 7 || outputs[0] != "alpha beta" {
			t.Fatal("a prior input or output borrower was retired")
		}
	}
	if snapshot := b.Snapshot(); snapshot.AllocationBytes <= snapshot.MeasuredRequiredBytes {
		t.Fatalf("flow and simultaneous peak were conflated: %+v", snapshot)
	}
	clear(outputs)
	outputs = nil
	for _, owner := range owners {
		owner.Release()
	}
	inputBacking = nil
	input.Release()
	if b.Snapshot().LiveBytes != 0 {
		t.Fatalf("final backing did not drain: %+v", b.Snapshot())
	}
	b.CloseRetired()
}

func TestBoundedTemplatePairKeepsTwoPassClaimsAndOutputs(t *testing.T) {
	b, _ := backend.NewBuildBounds(512, 512, 512)
	input, _ := b.OwnAllocation(8, 1)
	renderer, err := NewRenderer("system")
	if err != nil {
		t.Fatal(err)
	}
	var outputs []string
	var owners []*backend.OwnedAllocation
	for i := 0; i < 8; i++ {
		claims, userPasses := 0, 0
		s, u, err := renderPairBounded(b, renderer.system, nil, 512, func(m backend.PromptMeasure) error {
			claims++
			if m.SystemBytes != 6 || m.UserBytes != 4 || m.SystemJSONBytes != 8 || m.UserJSONBytes != 6 || userPasses != 1 {
				t.Fatalf("claim preceded measurement or changed bytes: %+v passes=%d", m, userPasses)
			}
			return nil
		}, func(out *textOutput) {
			userPasses++
			out.put("body")
		})
		if err != nil || claims != 1 || userPasses != 2 || s != "system" || u != "body" {
			t.Fatalf("pair semantics changed: s=%q u=%q claims=%d passes=%d err=%v", s, u, claims, userPasses, err)
		}
		outputs = append(outputs, s, u)
		owner, err := b.RetainedAllocation(10)
		if err != nil {
			t.Fatal(err)
		}
		owners = append(owners, owner)
		if b.Snapshot().LiveBytes != 8+int64(10*(i+1)) || outputs[0] != "system" {
			t.Fatalf("pair temporary survived or old output ended: %+v", b.Snapshot())
		}
	}
	clear(outputs)
	outputs = nil
	for _, owner := range owners {
		owner.Release()
	}
	input.Release()
	if b.Snapshot().LiveBytes != 0 {
		t.Fatal("pair output did not drain", b.Snapshot())
	}
	b.CloseRetired()
}

func TestBoundedTemplatePairFailureAndCancellationRetirePrivateBacking(t *testing.T) {
	for _, mode := range []string{"claim", "cancel", "user_second_pass"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b, _ := backend.NewBuildBoundsFromContext(ctx, 512, 512)
			input, _ := b.OwnAllocation(8, 1)
			renderer, err := NewRenderer("system")
			if err != nil {
				t.Fatal(err)
			}
			stop := errors.New("pair stopped")
			want := error(stop)
			passes := 0
			s, u, err := renderPairBounded(b, renderer.system, nil, 512, func(backend.PromptMeasure) error {
				if mode == "claim" {
					return stop
				}
				if mode == "cancel" {
					cancel()
				}
				return nil
			}, func(out *textOutput) {
				passes++
				out.put("body")
				if mode == "user_second_pass" && passes == 2 {
					out.err = stop
				}
			})
			if mode == "cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) || s != "" || u != "" || b.Snapshot().LiveBytes != 8 {
				t.Fatalf("failed pair kept dead backing or retired input: s=%q u=%q snapshot=%+v err=%v", s, u, b.Snapshot(), err)
			}
			if mode == "user_second_pass" && passes != 2 {
				t.Fatal("failure did not exercise the actual second pass")
			}
			input.Release()
			b.CloseRetired()
		})
	}
}

func TestBoundedTemplateListGrowthChargesOverlapAndRollsBack(t *testing.T) {
	for _, limit := range []int64{56, 104} {
		b, _ := backend.NewBuildBounds(0, 0, limit)
		input, err := b.OwnAllocation(8, 1)
		if err != nil {
			t.Fatal(err)
		}
		var values []string
		var owner *backend.OwnedAllocation
		for _, text := range []string{"a", "b"} {
			if err = appendTemplateValue(b, &values, &owner, text); err != nil {
				t.Fatal(err)
			}
		}
		err = appendTemplateValue(b, &values, &owner, "c")
		if limit == 56 {
			var capacity *backend.AllocationCapacityError
			if !errors.As(err, &capacity) || capacity.Used != 40 || capacity.Requested != 64 || len(values) != 2 || strings.Join(values, "") != "ab" || b.Snapshot().LiveBytes != 40 {
				t.Fatalf("growth did not preserve old backing: values=%v snapshot=%+v err=%v", values, b.Snapshot(), err)
			}
		} else if err != nil || strings.Join(values, "") != "abc" || b.Snapshot().LiveBytes != 72 || b.Snapshot().MeasuredRequiredBytes != 104 {
			t.Fatalf("true overlap was not counted: values=%v snapshot=%+v err=%v", values, b.Snapshot(), err)
		}
		clear(values)
		values = nil
		owner.Release()
		if b.Snapshot().LiveBytes != 8 {
			t.Fatal("list retirement released an unrelated input")
		}
		input.Release()
		if b.Snapshot().LiveBytes != 0 {
			t.Fatal("list owner did not drain")
		}
		b.CloseRetired()
	}
}

func TestBoundedTemplateFailureRetiresTreeAndWriter(t *testing.T) {
	for _, source := range []string{
		`prefix{{.missing}}suffix`,
		`{{define "x"}}text{{end}}{{template "x" .}}` + strings.Repeat("x", 128),
	} {
		b, _ := backend.NewBuildBounds(512, 512, 80)
		input, err := b.OwnAllocation(8, 1)
		if err != nil {
			t.Fatal(err)
		}
		_, err = RenderTemplateBounded(b, source, nil, 512)
		if err == nil || b.Snapshot().LiveBytes != 8 {
			t.Fatalf("failed tree kept backing or dropped input: snapshot=%+v err=%v", b.Snapshot(), err)
		}
		input.Release()
		b.CloseRetired()
	}
	// Direct writer failure covers the second pass after its fixed backing
	// was created, while the first pass succeeds without retaining any output.
	b, _ := backend.NewBuildBounds(64, 64)
	input, _ := b.OwnAllocation(8, 1)
	stop := errors.New("second pass stopped")
	passes := 0
	_, err := buildTextBounded(b, 64, func(out *textOutput) {
		passes++
		out.put("text")
		if passes == 2 {
			out.err = stop
		}
	})
	if !errors.Is(err, stop) || passes != 2 || b.Snapshot().LiveBytes != 8 {
		t.Fatalf("failed writer retained capacity: passes=%d snapshot=%+v err=%v", passes, b.Snapshot(), err)
	}
	input.Release()
	b.CloseRetired()
}
