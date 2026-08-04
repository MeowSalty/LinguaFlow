package service

import (
	"context"
	"errors"
	"testing"

	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
)

func TestGlossaryRuleQAEntrySemantics(t *testing.T) {
	client := testClient(t)
	ctx := context.Background()
	users := NewUserService(client, NewAuthService(client, AuthConfig{}, NewAdminService(client)))
	projects := NewProjectService(client, users)
	svc := NewGlossaryService(client, projects)
	u := createTestUser(t, client, "glossary-rules")
	p := createTestProject(t, client, "glossary-rules", u.ID)

	recommended, err := svc.CreateEntry(ctx, u.ID, p.ID, GlossaryEntryInput{Source: "API", Target: "接口"})
	if err != nil {
		t.Fatalf("create recommendation: %v", err)
	}
	if !recommended.Mandatory || recommended.Forbidden {
		t.Fatalf("manual CRUD defaults incorrect: %#v", recommended)
	}
	if _, err := svc.CreateEntry(ctx, u.ID, p.ID, GlossaryEntryInput{Source: "api", Target: "端点"}); !errors.Is(err, ErrGlossaryEntryExists) {
		t.Fatalf("second recommendation should conflict, got %v", err)
	}

	for _, target := range []string{"应用接口", "应用程序接口"} {
		entry, err := svc.CreateEntry(ctx, u.ID, p.ID, GlossaryEntryInput{
			Source:    "API",
			Target:    target,
			Forbidden: true,
		})
		if err != nil {
			t.Fatalf("create forbidden %q: %v", target, err)
		}
		if !entry.Forbidden || !entry.Mandatory {
			t.Fatalf("forbidden defaults incorrect: %#v", entry)
		}
	}
	if _, err := svc.CreateEntry(ctx, u.ID, p.ID, GlossaryEntryInput{Source: "API", Target: "应用接口", Forbidden: true}); !errors.Is(err, ErrGlossaryEntryExists) {
		t.Fatalf("duplicate forbidden target should conflict, got %v", err)
	}

	runtime, err := NewDatabaseGlossary(ctx, client, p)
	if err != nil {
		t.Fatalf("runtime glossary: %v", err)
	}
	hits, err := runtime.Lookup(ctx, "API", "en", "zh")
	if err != nil || len(hits) != 3 {
		t.Fatalf("runtime lookup: hits=%#v err=%v", hits, err)
	}

	if _, err := client.GlossaryEntry.Create().
		SetProjectID(p.ID).
		SetSourceKey("api").
		SetSource("API").
		SetTarget("并发推荐").
		Save(ctx); !ent.IsConstraintError(err) {
		t.Fatalf("database must enforce one recommendation per source, got %v", err)
	}

	added, err := runtime.Add(ctx, glossary.Entry{Source: "SDK", Target: "开发工具包", Mandatory: true})
	if err != nil || len(added.Added) != 1 {
		t.Fatalf("runtime add: result=%#v err=%v", added, err)
	}
	hits, err = runtime.Lookup(ctx, "Use the SDK", "en", "zh")
	if err != nil || len(hits) != 1 || hits[0].Target != "开发工具包" {
		t.Fatalf("runtime cache did not include added entry: hits=%#v err=%v", hits, err)
	}
}
