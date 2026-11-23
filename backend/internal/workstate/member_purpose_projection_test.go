package workstate

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/segment"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workitem"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/workrequest"
)

func memberPurposeFixture(t *testing.T) (context.Context, *ent.Client, *Store, Request, []Candidate, *storageSQLProbe) {
	t.Helper()
	ctx, client, scope, probe := scopeProjectionFixture(t)
	store := NewStore(client)
	client.WorkItem.Update().Where(workitem.JobRoundIDEQ(scope.RoundID)).SetRetryEpoch(scope.RetryEpoch).ExecX(ctx)
	first := client.Segment.Query().Where(segment.ResourceIDEQ(scope.ResourceID)).OnlyX(ctx)
	r, capture, candidates := integrityMemberPair(t, ctx, client, store, scope, first)
	integrityPublish(t, ctx, store, r, capture)
	client.Job.UpdateOneID(scope.JobID).SetExecutionConfig(map[string]any{"schema_version": 4, "defaults_version": 4, "response_format_version": 2, "unused": strings.Repeat("x", 4096)}).ExecX(ctx)
	client.WorkRequest.Update().Where(workrequest.IdentityEQ(r.ID)).SetMemberPool(7).SetMemberAttempt(3).SetMemberMode("translate").SetMemberFormat("json").SetSourceLang("en").SetTargetLang("zh").SetTextMode(true).ExecX(ctx)
	next, err := store.LoadCursor(ctx, scope, candidates[0].SegmentID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.PromoteMember(ctx, fixturePromotion(MemberPromotion{Scope: scope, Key: requestMemberKey(r, 0), Candidate: &candidates[0], Next: next})); err != nil {
		t.Fatal(err)
	}
	return ctx, client, store, r, candidates, probe
}

func purposeSQLCalls(t *testing.T, snapshot storageSQLSnapshot, table string) []storageSQLCall {
	t.Helper()
	var result []storageSQLCall
	for _, call := range snapshot.calls {
		if call.kind != "query" || !strings.Contains(call.query, "FROM `"+table+"`") {
			continue
		}
		actualTable, _ := scopeSelectedColumns(t, call.query)
		if actualTable == table {
			result = append(result, call)
		}
	}
	return result
}

func purposeSQLFields(t *testing.T, call storageSQLCall, columns []string, full storageSQLCall) {
	t.Helper()
	_, actual := scopeSelectedColumns(t, call.query)
	if !reflect.DeepEqual(actual, columns) || call.rows != 1 || call.valueBytes <= 0 || call.valueBytes >= full.valueBytes || len(call.query) >= len(full.query) {
		t.Fatalf("projection/actual return changed: columns=%v want=%v call=%+v full=%+v", actual, columns, call, full)
	}
	t.Logf("fields=%d rows=%d typed_bytes=%d full_fields_bytes=%d sql_bytes=%d full_sql_bytes=%d", len(actual), call.rows, call.valueBytes, full.valueBytes, len(call.query), len(full.query))
}

func TestMemberPurposeProjectionReadsRealSQLFacts(t *testing.T) {
	for _, kind := range []string{"input", "output", "promotion", "ack"} {
		t.Run(kind, func(t *testing.T) {
			ctx, client, store, r, candidates, probe := memberPurposeFixture(t)
			key := requestMemberKey(r, 1)
			probe.reset()
			fullRow := memberHeaderQuery(client.WorkRequest.Query().Where(workrequest.IdentityEQ(r.ID))).OnlyX(ctx)
			full := probe.stop()
			_, fullFields := scopeSelectedColumns(t, full.calls[0].query)
			if full.queries != 1 || full.rows != 1 || len(fullFields) != 36 {
				t.Fatal("complete header baseline changed", fullFields, full)
			}
			probe.reset()
			fullJob := client.Job.GetX(ctx, r.Scope.JobID)
			fullJobSQL := probe.stop()
			if fullRow.RetryEpoch == 0 || fullRow.PromotionCount != 2 || fullRow.PromotionCursor != 1 || fullRow.PromotionState != "business_ready" || fullRow.Identity == "" || fullRow.SnapshotDigest == "" || fullRow.FreezeDigest == "" || fullRow.ResponseDigest == "" {
				t.Fatal("projection fixture lacks nonzero dependencies")
			}
			var headerRows int64
			recorded := backend.WithMemberWorkRecorder(ctx, func(e backend.MemberWorkEvent) {
				if e.Phase == "member" && e.Operation == "header_read" {
					headerRows += e.Rows
				}
			})
			var err error
			probe.reset()
			switch kind {
			case "input":
				var m InputMember
				m, err = store.LoadInputMember(recorded, key, 4096)
				if err == nil {
					if m.Key != key || m.SegmentID != candidates[1].SegmentID || m.SourceGeneration != r.Scope.SourceGeneration || !sameInt(m.SourceRevisionID, r.Scope.SourceRevisionID) || string(m.Payload) != string(r.InputMembers[1].Payload) {
						t.Fatal("partial header escaped into input identity or payload")
					}
					m.Release()
				}
			case "output":
				var m OutputMember
				m, err = store.LoadOutputMember(recorded, key, true, 4096)
				if err == nil {
					if m.Key != key || m.Bytes <= 0 || m.Digest == "" || string(m.Payload) != string(integrityOutput(r, 1, "member-1").Payload) {
						t.Fatal("partial header escaped into output identity or payload")
					}
					m.Release()
				}
			case "promotion":
				next, e := store.LoadCursor(ctx, r.Scope, candidates[1].SegmentID)
				if e != nil {
					t.Fatal(e)
				}
				probe.reset()
				err = store.promoteMember(recorded, fixturePromotion(MemberPromotion{Scope: r.Scope, Key: key, Candidate: &candidates[1], Next: next}))
			case "ack":
				proof := memberPromotionProof{Identity: ResponseIdentityFromRow(fullRow), Key: key, ResponseDigest: fullRow.ResponseDigest}
				err = Transaction(ctx, client, func(tx *ent.Client) error {
					if e := LockJob(ctx, tx, r.Scope.JobID); e != nil {
						return e
					}
					ok, e := proof.exists(ctx, tx)
					if e == nil && ok {
						t.Fatal("pending ordinal was acknowledged as committed")
					}
					return e
				})
			}
			actual := probe.stop()
			if err != nil {
				t.Fatal(err)
			}
			calls := purposeSQLCalls(t, actual, "work_requests")
			access := []string{"id", "retry_epoch", "response_format_version", "promotion_count", "promotion_state"}
			identity := []string{"id", "identity", "job_id", "resource_id", "job_round_id", "retry_epoch", "response_format_version", "promotion_count", "snapshot_digest", "freeze_digest", "response_digest"}
			state := append(append([]string(nil), identity...), "promotion_cursor", "promotion_state")
			want := [][]string{access}
			switch kind {
			case "promotion":
				// Exec keeps the generated read and hook/NotFound path, but only
				// returns the ID needed by hooks instead of discarded payloads.
				want = [][]string{identity, state, {workrequest.FieldID}}
			case "ack":
				want = [][]string{state}
			}
			if len(calls) != len(want) || actual.begins != actual.commits || (kind != "ack" && headerRows != 1) || (kind == "input" || kind == "output") && (actual.queries != 3 || actual.rows != 3) {
				t.Fatalf("physical reads or header rows changed: kind=%s snapshot=%+v observed=%d", kind, actual, headerRows)
			}
			for i, fields := range want {
				purposeSQLFields(t, calls[i], fields, full.calls[0])
			}
			if kind == "promotion" {
				jobCalls := purposeSQLCalls(t, actual, "jobs")
				var pauseReads, bindingReads int
				for _, call := range jobCalls {
					_, fields := scopeSelectedColumns(t, call.query)
					if reflect.DeepEqual(fields, []string{"id", "status", "pause_requested"}) {
						purposeSQLFields(t, call, fields, fullJobSQL.calls[0])
						pauseReads++
					} else if reflect.DeepEqual(fields, []string{"id", "pipeline_runtime_state"}) {
						purposeSQLFields(t, call, fields, fullJobSQL.calls[0])
						bindingReads++
					}
				}
				if pauseReads != 1 || bindingReads != 2 || client.WorkCandidate.Query().CountX(ctx) != 2 {
					t.Fatal("pause/transfer/final clear read or candidate ownership changed", jobCalls)
				}
			}
			// Public headers still receive the complete accounting and context.
			h, e := store.ResponseHeader(ctx, r.ID)
			row := client.WorkRequest.Query().Where(workrequest.IdentityEQ(r.ID)).OnlyX(ctx)
			if e != nil || !reflect.DeepEqual(h, responseHeader(row)) || h.MemberPool != 7 || h.MemberAttempt != 3 || !h.TextMode || client.Job.GetX(ctx, r.Scope.JobID).ExecutionConfig["unused"] != fullJob.ExecutionConfig["unused"] {
				t.Fatal("a partial row reached public headers or changed the unused definition", e)
			}
		})
	}
}

func TestMemberAccessProjectionRejectsKeyAndPhaseDrift(t *testing.T) {
	for _, kind := range []string{"epoch", "negative_ordinal", "end_ordinal", "format", "input_phase", "output_phase", "final_phase", "missing", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			ctx, client, store, r, _, probe := memberPurposeFixture(t)
			key := requestMemberKey(r, 1)
			want := ErrManifest
			switch kind {
			case "epoch":
				key.CaptureEpoch--
			case "negative_ordinal":
				key.Ordinal = -1
			case "end_ordinal":
				key.Ordinal = 2
			case "format":
				client.WorkRequest.Update().Where(workrequest.IdentityEQ(r.ID)).SetResponseFormatVersion(1).ExecX(ctx)
			case "input_phase":
				client.WorkRequest.Update().Where(workrequest.IdentityEQ(r.ID)).SetPromotionState("promoted").ExecX(ctx)
			case "output_phase":
				client.WorkRequest.Update().Where(workrequest.IdentityEQ(r.ID)).SetPromotionState("inputs_ready").ExecX(ctx)
			case "final_phase":
				client.WorkRequest.Update().Where(workrequest.IdentityEQ(r.ID)).SetPromotionState("outputs_ready").ExecX(ctx)
			case "missing":
				key.RequestID = "absent-request"
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx, want = cancelled, context.Canceled
			}
			probe.reset()
			var err error
			if kind == "output_phase" || kind == "final_phase" {
				_, err = store.LoadOutputMember(ctx, key, true, 4096)
			} else {
				_, err = store.LoadInputMember(ctx, key, 4096)
			}
			actual := probe.stop()
			if kind == "missing" {
				if !ent.IsNotFound(err) {
					t.Fatal("missing row classification changed", err)
				}
			} else if !errors.Is(err, want) {
				t.Fatalf("wrong key/phase failure: %v want=%v", err, want)
			}
			for _, call := range actual.calls {
				if call.kind != "query" || !strings.Contains(call.query, "FROM `work_requests`") {
					t.Fatal("invalid key/phase reached a payload or write", call)
				}
			}
		})
	}
}

func TestRuntimePurposeProjectionPreservesLockedBindings(t *testing.T) {
	for _, kind := range []string{"null", "transfer", "other_owner", "clear_progress", "clear_response_business", "rollback"} {
		t.Run(kind, func(t *testing.T) {
			ctx, client, scope, probe := scopeProjectionFixture(t)
			state := backend.PipelineRuntimeState{Version: 1, ProgressID: "previous", ResponseID: "response", BusinessOwner: "response", ResourceID: scope.ResourceID, RoundIndex: 3}
			if kind != "null" {
				if err := Transaction(ctx, client, func(tx *ent.Client) error { return saveRuntimeState(ctx, tx, scope.JobID, state) }); err != nil {
					t.Fatal(err)
				}
			} else {
				state = backend.PipelineRuntimeState{Version: 1}
			}
			probe.reset()
			before := client.Job.GetX(ctx, scope.JobID)
			full := probe.stop()
			expected := state
			rollback := errors.New("rollback after binding write")
			probe.reset()
			err := Transaction(ctx, client, func(tx *ent.Client) error {
				if err := LockJob(ctx, tx, scope.JobID); err != nil {
					return err
				}
				switch kind {
				case "null", "other_owner":
					return transferRuntimeBindingTx(ctx, tx, scope.JobID, "absent", "next")
				case "transfer", "rollback":
					expected.ProgressID = "next"
					if err := transferRuntimeBindingTx(ctx, tx, scope.JobID, "previous", "next"); err != nil {
						return err
					}
					if kind == "rollback" {
						return rollback
					}
				case "clear_progress":
					expected.ProgressID, expected.ResponseID = "", ""
					return clearRuntimeBindingTx(ctx, tx, scope.JobID, "previous", false)
				case "clear_response_business":
					expected.ResponseID, expected.BusinessOwner = "", ""
					return clearRuntimeBindingTx(ctx, tx, scope.JobID, "response", true)
				}
				return nil
			})
			actual := probe.stop()
			if kind == "rollback" {
				if !errors.Is(err, rollback) || actual.commits != 0 {
					t.Fatal("binding rollback was hidden", err)
				}
				expected = state
			} else if err != nil || actual.commits != 1 {
				t.Fatal("binding update failed", err)
			}
			calls := purposeSQLCalls(t, actual, "jobs")
			reads := int64(2)
			if kind == "null" || kind == "other_owner" {
				reads = 1
			}
			if int64(len(calls)) != reads || actual.queries != reads || actual.rows != reads || actual.begins != 1 || actual.calls[0].kind != "exec" || !strings.HasPrefix(actual.calls[0].query, "UPDATE `jobs`") {
				t.Fatal("binding projection changed the lock or physical reads", actual)
			}
			purposeSQLFields(t, calls[0], []string{"id", "pipeline_runtime_state"}, full.calls[0])
			if reads == 2 {
				purposeSQLFields(t, calls[1], []string{"id"}, full.calls[0])
			}
			after := client.Job.GetX(ctx, scope.JobID)
			loaded, e := decodeRuntimeState(after.PipelineRuntimeState)
			if e != nil || loaded != expected || after.Status != before.Status || after.RetryEpoch != before.RetryEpoch || after.PauseRequested != before.PauseRequested || after.ErrorMessage != nil || !reflect.DeepEqual(after.ExecutionConfig, before.ExecutionConfig) || kind == "rollback" && !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Fatal("partial job changed binding, nullable facts or rollback", loaded, expected, e)
			}
		})
	}
}
