package api

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/qa"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

func TestPlatformResponseTimesUseUTCAndKeepPrecision(t *testing.T) {
	local := time.Date(2026, 9, 29, 12, 30, 5, 123456789, time.FixedZone("user", 8*60*60))
	const want = "2026-09-29T04:30:05.123456789Z"
	job := &ent.Job{CreatedAt: local, UpdatedAt: local, StartedAt: &local, Edges: ent.JobEdges{Project: &ent.Project{Name: "project"}}}
	summary, err := toJobSummary(job)
	if err != nil {
		t.Fatal(err)
	}
	resource := &ent.Resource{CreatedAt: local, UpdatedAt: local}
	segment := &ent.Segment{CreatedAt: local, UpdatedAt: local}
	tests := []struct {
		name   string
		value  any
		fields []string
	}{
		{"audit", toAdminAuditLogItem(&ent.ActivityLog{CreatedAt: local}), []string{"created_at"}},
		{"resource", toResourceResponse(resource, 0, 0), []string{"created_at", "updated_at"}},
		{"generated resource", toGeneratedResource(resource, 0, 0), []string{"created_at", "updated_at"}},
		{"segment", toSegmentResponse(segment), []string{"created_at", "updated_at"}},
		{"generated segment", toOpenAPISegment(segment), []string{"created_at", "updated_at"}},
		{"job summary", summary, []string{"created_at", "updated_at", "started_at"}},
		{"event", jobEventFromEvent(event.Event{CreatedAt: local}), []string{"created_at"}},
		{"sync", convertSyncTaskToStatusResponse(&ent.SyncTask{CancelledAt: &local}), []string{"cancelled_at"}},
		{"quality issue", toOpenAPIQualityIssue(qa.QualityIssue{DecidedAt: &local}), []string{"decided_at"}},
		{"template", entTranslationPromptTemplateToResponse(&ent.TranslationPromptTemplate{CreatedAt: local, UpdatedAt: local}), []string{"created_at", "updated_at"}},
		{"translation preview", toSegmentTranslationPreviewResponse(&service.PreviewOutput{ApplyToken: "token", ApplyExpiresAt: local}), []string{"apply_expires_at"}},
		{"session", newAuthSessionResponse(&service.Session{AccessExpiresAt: local, RefreshExpiresAt: local, User: &ent.User{}}), []string{"expires_at", "refresh_expires_at"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatal(err)
			}
			var values map[string]any
			if err := json.Unmarshal(encoded, &values); err != nil {
				t.Fatal(err)
			}
			for _, field := range tt.fields {
				if values[field] != want {
					t.Errorf("%s = %v, want %s", field, values[field], want)
				}
			}
		})
	}
	if local.Location() == time.UTC || summary.StartedAt == &local {
		t.Fatal("response conversion must preserve caller's time and copy pointers")
	}
	if got := timePtrToString(&local); got == nil || *got != want {
		t.Fatalf("legacy nullable string time = %v", got)
	}
}

func TestResponseOptionalTimesPreserveAbsence(t *testing.T) {
	if timePtrToString(nil) != nil {
		t.Fatal("nil timestamp became a string")
	}
	template := entTranslationPromptTemplateToResponse(&ent.TranslationPromptTemplate{})
	if template.CreatedAt != nil || template.UpdatedAt != nil {
		t.Fatal("zero template times must stay omitted")
	}
	summary, err := toJobSummary(&ent.Job{Edges: ent.JobEdges{Project: &ent.Project{}}})
	if err != nil || summary.StartedAt != nil {
		t.Fatalf("absent job start changed: %+v, %v", summary, err)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["started_at"]) != "null" {
		t.Fatalf("job start wire value = %s, want null", fields["started_at"])
	}
	if toOpenAPIQualityIssue(qa.QualityIssue{}).DecidedAt != nil || convertSyncTaskToStatusResponse(&ent.SyncTask{}).CancelledAt != nil {
		t.Fatal("absent decision/cancellation time changed")
	}
}
