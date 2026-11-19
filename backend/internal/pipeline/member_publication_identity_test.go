package pipeline

import (
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

func TestMemberMemoryPublicationIdentity(t *testing.T) {
	for _, fault := range []string{"epoch", "prompt", "candidate", "index", "length", "digest"} {
		t.Run(fault, func(t *testing.T) {
			store := NewMemoryRoundStore(nil)
			defer store.Close()
			in := RequestIntent{ID: "identity", Stage: backend.RequestStageMain, InputDigest: "identity-digest", Indices: []int{0, 1}}
			for ordinal, m := range memberFixtureInputs(2) {
				p, a := memberFixturePayload(t, t.Context(), m)
				in.InputMembers = append(in.InputMembers, InputMemberFrame{Key: MemberKey{RequestID: in.ID, Ordinal: ordinal}, ResponseMember: ResponseMember{Index: ordinal, CandidateID: m.CandidateID}, PromptID: m.PromptID, Payload: p, Allocation: a, Bytes: int64(len(p)), Digest: responseDigest(p)})
			}
			m := &in.InputMembers[1]
			switch fault {
			case "epoch":
				m.Key.CaptureEpoch = 1
			case "prompt":
				m.PromptID = in.InputMembers[0].PromptID
			case "candidate":
				m.CandidateID = in.InputMembers[0].CandidateID
			case "index":
				m.Index = 0
			case "length":
				m.Bytes--
			case "digest":
				m.Digest = "bad"
			}
			if err := store.Reserve(t.Context(), in); err == nil {
				t.Fatalf("accepted invalid frozen identity: %s", fault)
			}
			if len(store.requests) != 0 {
				t.Fatal("partial frozen collection published")
			}
		})
	}
}

func TestMemberMemoryIntegrityKeepsValidSibling(t *testing.T) {
	for _, fault := range []string{"header_format", "header_unpublished", "ordinal", "epoch", "input_digest", "input_length", "output_digest", "output_length"} {
		t.Run(fault, func(t *testing.T) {
			store := NewMemoryRoundStore(nil)
			defer store.Close()
			head := memberFixtureReserve(t, t.Context(), store, "integrity", 4096, memberFixtureInputs(2))
			head = memberFixtureOutputs(t, t.Context(), store, head, []archivedOutput{{Target: "first"}, {Target: "second"}}, nil)
			if _, err := store.PublishBusiness(t.Context(), head.ID, glossary.ResponseBusiness{}, nil); err != nil {
				t.Fatal(err)
			}
			r := store.requests[head.ID]
			key := archiveMemberKey(head, 1)
			input := r.inputs[1]
			output := r.outputs[1]
			oldHeader := r.response.MainResponseHeader
			switch fault {
			case "header_format":
				r.response.FormatVersion = 1
			case "header_unpublished":
				r.response.Phase = "inputs_pending"
			case "ordinal":
				key.Ordinal = 2
			case "epoch":
				key.CaptureEpoch++
			case "input_digest":
				input.Digest = "bad"
				r.inputs[1] = input
			case "input_length":
				input.Bytes--
				r.inputs[1] = input
			case "output_digest":
				output.Digest = "bad"
				r.outputs[1] = output
			case "output_length":
				output.Bytes--
				r.outputs[1] = output
			}
			var err error
			if fault == "output_digest" || fault == "output_length" {
				_, err = store.LoadOutputMember(t.Context(), key, true, 4096)
			} else {
				_, err = store.LoadInputMember(t.Context(), key, 4096)
			}
			if err == nil {
				t.Fatalf("corrupt member accepted: %s", fault)
			}
			r.response.MainResponseHeader = oldHeader
			valid, err := store.LoadOutputMember(t.Context(), archiveMemberKey(head, 0), true, 4096)
			if err != nil {
				t.Fatalf("valid sibling lost: %v", err)
			}
			defer valid.Release()
			if got := memberFixtureDecode(t, valid).Target; got != "first" {
				t.Fatalf("valid sibling changed: %q", got)
			}
		})
	}
}
