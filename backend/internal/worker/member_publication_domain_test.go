package worker

import (
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/engine"
	"github.com/MeowSalty/LinguaFlow/backend/internal/execution"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/pipeline"
	"github.com/MeowSalty/LinguaFlow/backend/internal/progress"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
)

type memberDomainStore struct {
	pipeline.RoundStore
	domain responsecontract.PublicationDomain
}

func (s *memberDomainStore) PublicationDomain() responsecontract.PublicationDomain { return s.domain }

func TestMemberPublicationCapabilityRejectedBeforeDispatch(t *testing.T) {
	for _, mode := range []string{"sql_file", "memory_persist_sql", "sql_wrong_authority", "sql_wrong_project", "sql_wrong_job", "same_sql", "memory_file", "no_inline"} {
		t.Run(mode, func(t *testing.T) {
			f, http := newHandoffHTTPFixture(t, "translate", 1, 1, 1)
			http.open()
			project := f.client.Job.QueryProject(f.client.Job.GetX(t.Context(), f.jobID)).OnlyX(t.Context())
			var dictionary glossary.Glossary = glossary.NewMemory()
			if mode != "sql_file" && mode != "memory_file" {
				g, err := service.NewDatabaseGlossary(service.WithExecutionJobActor(t.Context(), f.ownerID, f.jobID), f.client, project)
				if err != nil {
					t.Fatal(err)
				}
				dictionary = g
			}
			inline := execution.DefaultInlineTermExtraction()
			inline.Enabled = mode != "no_inline"
			f.snapshot.Rounds[0].Translate.InlineTermExtraction = &inline
			f.snapshot.GlossaryEnabled = true
			f.snapshot = completeWorkerSnapshot(t, f.snapshot)
			f.persistSnapshot(t)
			r, err := NewExecutionRuntime(f.snapshot, f.pool, nil, pipeline.DefaultCandidateLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			eng, err := f.factory.BuildEngine(pipeline.WithExecutionRuntime(t.Context(), r), f.snapshot, engine.RuntimeResources{Glossary: dictionary}, progress.Nop{})
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			doc, base := f.round(t, 0)
			base.publicationProjectID = project.ID
			eng.PrepareDocument(doc, nil)
			var store pipeline.RoundStore = base
			if mode == "memory_persist_sql" || mode == "memory_file" {
				memory := pipeline.NewMemoryRoundStore(nil)
				defer memory.Close()
				store = memory
			}
			if mode == "sql_wrong_authority" || mode == "sql_wrong_project" || mode == "sql_wrong_job" {
				domain := base.PublicationDomain()
				switch mode {
				case "sql_wrong_authority":
					domain.Authority = responsecontract.NewPublicationAuthority()
				case "sql_wrong_project":
					domain.ProjectID++
				case "sql_wrong_job":
					domain.JobID++
				}
				store = &memberDomainStore{RoundStore: base, domain: domain}
			}
			before := f.client.GlossaryEntry.Query().CountX(t.Context())
			result, err := pipeline.RunStagedRound(t.Context(), eng.Rounds()[0], doc, store, r, f.logger, progress.Nop{})
			legal := mode == "same_sql" || mode == "memory_file" || mode == "no_inline"
			http.mu.Lock()
			calls := http.calls
			http.mu.Unlock()
			if legal {
				if err != nil || calls != 1 || len(result.Resolved) != 1 {
					t.Fatalf("legal publication rejected: calls=%d resolved=%d err=%v", calls, len(result.Resolved), err)
				}
			} else {
				if !errors.Is(err, responsecontract.ErrPublicationDomain) || calls != 0 || f.client.UsageRecord.Query().CountX(t.Context()) != 0 || f.client.WorkRequest.Query().CountX(t.Context()) != 0 {
					t.Fatalf("incompatible capability dispatched or wrote: calls=%d err=%v", calls, err)
				}
			}
			if f.client.GlossaryEntry.Query().CountX(t.Context()) != before {
				t.Fatal("domain validation changed dictionary")
			}
		})
	}
}
