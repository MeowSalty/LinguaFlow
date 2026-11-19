package response

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMemberResponsePureContract(t *testing.T) {
	t.Run("outcome", func(t *testing.T) {
		key := MemberKey{RequestID: "request", Ordinal: 16, CaptureEpoch: 7}
		valid := MemberOutcome{Key: key, Reason: MemberTooLarge, Limit: 4096, Observed: 8192}
		if _, err := valid.Resolve(key, false); err != nil || valid.Code() != 1 {
			t.Fatal(err)
		}
		for _, mutate := range []func(*MemberOutcome){
			func(o *MemberOutcome) { o.Key.RequestID = "other" }, func(o *MemberOutcome) { o.Key.Ordinal++ }, func(o *MemberOutcome) { o.Key.CaptureEpoch++ }, func(o *MemberOutcome) { o.Accepted = true }, func(o *MemberOutcome) { o.Reason = "" }, func(o *MemberOutcome) { o.Reason = "unknown" }, func(o *MemberOutcome) { o.Limit = -1 }, func(o *MemberOutcome) { o.Observed = -1 },
		} {
			o := valid
			mutate(&o)
			if _, err := o.Resolve(key, false); err == nil {
				t.Fatalf("accepted invalid outcome %+v", o)
			}
		}
		accepted := MemberOutcome{Key: key, Accepted: true}
		if _, err := accepted.Resolve(key, true); err != nil {
			t.Fatal(err)
		}
		for _, mutate := range []func(*MemberOutcome){func(o *MemberOutcome) { o.Reason = MemberInvalid }, func(o *MemberOutcome) { o.Limit = 1 }, func(o *MemberOutcome) { o.Observed = 1 }} {
			o := accepted
			mutate(&o)
			if _, err := o.Resolve(key, true); err == nil {
				t.Fatalf("accepted rejection facts %+v", o)
			}
		}
	})
	t.Run("phase", func(t *testing.T) {
		phases := []ResponsePhase{InputsReady, CapturePending, OutputsReady, BusinessReady, Promoted, ResponseCancelled}
		for i, from := range phases {
			for j, to := range phases {
				want := i == j || i < 4 && (j == i+1 || to == ResponseCancelled)
				if got := from.CanAdvance(to); got != want {
					t.Fatalf("%s -> %s = %v want %v", from, to, got, want)
				}
			}
		}
		if ResponsePhase("unknown").CanAdvance(ResponseCancelled) {
			t.Fatal("unknown phase advanced")
		}
	})
	t.Run("facts", func(t *testing.T) {
		valid := ResponseFacts{OutputMemberBytes: 3, ModelOutputBytes: 1, CorrectionBytes: 2, PublicKind: PublicNone}
		if err := valid.Validate(); err != nil {
			t.Fatal(err)
		}
		for _, f := range []ResponseFacts{
			{OutputMemberBytes: math.MinInt64, ModelOutputBytes: math.MaxInt64, CorrectionBytes: 1, PublicKind: PublicNone},
			{OutputMemberBytes: 1, ModelOutputBytes: 2, PublicKind: PublicNone},
			{ModelOutputBytes: math.MaxInt64, CorrectionBytes: 1, PublicKind: PublicNone},
			{CaptureBytes: -1, PublicKind: PublicNone},
			{PublicBytes: 1, PublicKind: PublicNone},
			{PublicKind: PublicKind("unknown")},
		} {
			if f.Validate() == nil {
				t.Fatalf("accepted invalid facts %+v", f)
			}
		}
	})
	t.Run("domain", func(t *testing.T) {
		owner := new(int)
		a := RegisterPublicationAuthority(owner)
		if a != RegisterPublicationAuthority(owner) || RegisterPublicationAuthority([]int{1}) != nil {
			t.Fatal("authority registration is unstable")
		}
		good := PublicationDomain{Medium: PublicationSQL, Authority: a, JobID: 1, ProjectID: 2}
		if err := ValidatePublication(good, good); err != nil {
			t.Fatal(err)
		}
		memory := PublicationDomain{Medium: PublicationMemory, Authority: a}
		if err := ValidatePublication(memory, memory); err != nil {
			t.Fatal(err)
		}
		for _, invalid := range []PublicationDomain{{Medium: PublicationSQL, Authority: a, JobID: 1}, {Medium: PublicationSQL, Authority: a, ProjectID: 2}, {Medium: PublicationMemory}} {
			if !errors.Is(ValidatePublication(invalid, invalid), ErrPublicationDomain) {
				t.Fatal("invalid scope accepted")
			}
		}
		for _, wrong := range []PublicationDomain{{Medium: PublicationMemory, Authority: a}, {Medium: PublicationSQL, Authority: a, JobID: 2, ProjectID: 2}, {Medium: PublicationSQL, Authority: NewPublicationAuthority(), JobID: 1, ProjectID: 2}} {
			if !errors.Is(ValidatePublication(good, wrong), ErrPublicationDomain) {
				t.Fatal("accepted mismatched publication")
			}
		}
	})
	t.Run("install_once", func(t *testing.T) {
		var count atomic.Int32
		p := &PreparedPublication{Install: func() { count.Add(1) }}
		var wg sync.WaitGroup
		for range 32 {
			wg.Go(func() {
				if err := p.InstallOnce(); err != nil {
					t.Error(err)
				}
				_ = p.Installed()
			})
		}
		wg.Wait()
		if count.Load() != 1 || !p.Installed() {
			t.Fatal("installation did not settle once")
		}
		if (&PreparedPublication{}).InstallOnce() == nil {
			t.Fatal("empty installation accepted")
		}
	})
}
