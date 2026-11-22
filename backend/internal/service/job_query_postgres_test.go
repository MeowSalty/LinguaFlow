package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestPostgresJobTimeBoundariesAndSessionExpiry(t *testing.T) {
	dsn := os.Getenv("LINGUAFLOW_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("LINGUAFLOW_TEST_POSTGRES_DSN is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	connection, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid PostgreSQL test DSN")
	}
	admin := stdlib.OpenDB(*connection)
	defer admin.Close()
	// Session expiry seeds the fixed password connection and local policy.
	// Isolate all fixture state so coverage and race can reuse the same DSN.
	schema := fmt.Sprintf("job_time_%d_%d", os.Getpid(), time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("remove isolated job/time test schema: %v", err)
		}
	}()
	scopedDSN, err := jobTimeSchemaDSN(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated PostgreSQL job/time schema: %s", schema)
	cfg := config.DefaultServerConfig()
	cfg.Database = config.DatabaseConfig{Driver: config.DatabaseDriverPostgres, DSN: scopedDSN, MaxOpenConns: 3, MaxIdleConns: 2}
	db, client, err := database.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	unlock, err := database.AcquireMigrationLock(ctx, db, config.DatabaseDriverPostgres)
	if err != nil {
		t.Fatal(err)
	}
	err = client.Schema.Create(ctx)
	unlockErr := unlock()
	if err != nil {
		t.Fatal(err)
	}
	if unlockErr != nil {
		t.Fatal(unlockErr)
	}
	suffix := fmt.Sprintf("pg-time-%d", time.Now().UnixNano())
	actor := createTestUser(t, client, suffix)
	project := createTestProject(t, client, suffix, actor.ID)
	svc := newJobRoundTestService(t, client, nil)
	base := time.Date(2026, 9, 29, 12, 0, 0, 123456000, time.UTC)
	older := seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", base)
	tie := seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", base.In(time.FixedZone("east", 8*60*60)))
	newer := seedQueryJob(t, client, project.ID, JobStatusFailed, "manual", base.Add(time.Microsecond))
	var cursor string
	for _, want := range []int{newer.ID, tie.ID, older.ID} {
		page, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{State: "all", Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		assertQueryJobIDs(t, page, want)
		cursor = page.NextCursor
	}
	if cursor != "" {
		t.Fatal("last page has continuation")
	}
	between := base.Add(time.Nanosecond).In(time.FixedZone("west", -7*60*60))
	before, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{State: "all", UpdatedBefore: &between})
	if err != nil {
		t.Fatal(err)
	}
	assertQueryJobIDs(t, before, tie.ID, older.ID)
	after, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{State: "all", UpdatedFrom: &between})
	if err != nil {
		t.Fatal(err)
	}
	assertQueryJobIDs(t, after, newer.ID)
	summary, err := svc.getJobsSummary(ctx, actor.ID, JobSummaryOptions{}, between)
	if err != nil || summary.RecentFailed != 2 || !summary.AsOf.Equal(between) {
		t.Fatalf("fractional upper summary=%+v, err=%v", summary, err)
	}
	summary, err = svc.getJobsSummary(ctx, actor.ID, JobSummaryOptions{}, between.Add(7*24*time.Hour))
	if err != nil || summary.RecentFailed != 1 {
		t.Fatalf("fractional lower summary=%+v, err=%v", summary, err)
	}
	invalidCursor, err := encodeAccessibleJobCursor(&ent.Job{ID: newer.ID, UpdatedAt: between})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListAccessibleJobs(ctx, actor.ID, AccessibleJobListOptions{Cursor: invalidCursor}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid precision error=%v", err)
	}
	assertSessionExpiryMatchesCredentials(t, client, actor)
}

// Preserve the pgx/libpq connection string grammar, which differs from URL
// form encoding: '+' is literal and raw semicolons are allowed in URI values.
func jobTimeSchemaDSN(dsn, schema string) (string, error) {
	if _, err := pgx.ParseConfig(dsn); err != nil {
		return "", errors.New("invalid PostgreSQL test DSN")
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		// libpq permits literal '?' in userinfo. Skip it and the complete
		// bracketed host list before looking for the actual query separator.
		i := strings.Index(dsn, "://") + 3
		if end := strings.IndexAny(dsn[i:], "@/"); end >= 0 && dsn[i+end] == '@' {
			i += end + 1
		}
		for i < len(dsn) {
			if dsn[i] == '[' {
				i += strings.IndexByte(dsn[i:], ']') + 1
			} else {
				for i < len(dsn) && !strings.ContainsRune(":/?,", rune(dsn[i])) {
					i++
				}
			}
			if i < len(dsn) && dsn[i] == ':' {
				i++
				for i < len(dsn) && !strings.ContainsRune("/?,", rune(dsn[i])) {
					i++
				}
			}
			if i == len(dsn) || dsn[i] != ',' {
				break
			}
			i++
		}
		if i < len(dsn) && dsn[i] == '/' {
			if query := strings.IndexByte(dsn[i:], '?'); query >= 0 {
				i += query
			} else {
				i = len(dsn)
			}
		}
		prefix, query := dsn, ""
		if i < len(dsn) && dsn[i] == '?' {
			prefix, query = dsn[:i], dsn[i+1:]
		}
		var parameters []string
		for _, parameter := range strings.Split(query, "&") {
			if parameter == "" {
				continue
			}
			key, _, _ := strings.Cut(parameter, "=")
			key, err := url.PathUnescape(strings.Trim(key, " "))
			if err != nil {
				return "", errors.New("invalid PostgreSQL test parameter")
			}
			if !strings.EqualFold(key, "search_path") {
				parameters = append(parameters, parameter)
			}
		}
		parameters = append(parameters, "search_path="+url.PathEscape(schema))
		return prefix + "?" + strings.Join(parameters, "&"), nil
	}

	space := func(b byte) bool { return strings.ContainsRune(" \t\n\r\v\f", rune(b)) }
	var parameters []string
	for i := 0; i < len(dsn); {
		for i < len(dsn) && space(dsn[i]) {
			i++
		}
		if i == len(dsn) {
			break
		}
		start := i
		equals := i + strings.IndexByte(dsn[i:], '=')
		key := strings.Trim(dsn[start:equals], " \t\n\r\v\f")
		i = equals + 1
		for i < len(dsn) && space(dsn[i]) {
			i++
		}
		valueStart := i
		trailingEscape := false
		if i < len(dsn) && dsn[i] == '\'' {
			i++
			for i < len(dsn) {
				if dsn[i] == '\\' {
					i += 2
				} else if dsn[i] == '\'' {
					i++
					break
				} else {
					i++
				}
			}
		} else {
			for i < len(dsn) && !space(dsn[i]) {
				if dsn[i] == '\\' {
					i++
					if i == len(dsn) {
						trailingEscape = true
						break
					}
				}
				i++
			}
		}
		end := i
		if trailingEscape {
			// libpq ignores a final unquoted backslash. Do not let it consume
			// the separator before the newly appended search_path parameter.
			end--
		}
		if !strings.EqualFold(key, "search_path") {
			parameter := dsn[start:end]
			if end == valueStart {
				// An unquoted empty final value must stay empty when another
				// parameter follows instead of consuming its key as the value.
				parameter = dsn[start:equals+1] + "''"
			}
			parameters = append(parameters, parameter)
		}
	}
	parameters = append(parameters, "search_path='"+strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(schema)+"'")
	return strings.Join(parameters, " "), nil
}

func TestPostgresJobTimeSchemaDSNPreservesConnectionOptions(t *testing.T) {
	const schema = "job_time_fixture"
	const keyword = "host=localhost user=fixture dbname=fixture sslmode=disable "
	tests := []struct {
		name string
		dsn  string
	}{
		{"URL", "postgres://fixture@localhost/fixture?sslmode=disable"},
		{"URL encoded spaces", "postgresql://fixture:a%20b@localhost/fixture?sslmode=disable&options=-c%20timezone%3DUTC&application_name=job%20time"},
		{"URL literal plus and semicolon", "postgres://fixture@localhost/fixture?sslmode=disable&password=a+b;c&application_name=one;two"},
		{"URL repeated and case variants", "postgres://fixture@localhost/fixture?sslmode=disable&application_name=one&application_name=two&Search_Path=public&search%5fpath=old"},
		{"URL database aliases", "postgres://fixture@localhost/fixture?sslmode=disable&dbname=first&database=second&dbname=last"},
		{"URL question mark in userinfo without query", "postgres://fixture:p?ass@localhost/fixture"},
		{"URL question mark in userinfo with query", "postgres://fixture:p?ass@localhost/fixture?sslmode=disable&Search_Path=public"},
		{"URL raw spaced key", "postgres://fixture@localhost/fixture?sslmode=disable& Search_Path =public"},
		{"URL bracketed host list", "postgres://fixture@[::1]:5432,localhost:5433/fixture?sslmode=disable&Search_Path=public"},
		{"keyword", keyword + "search_path=public"},
		{"keyword quoted values", keyword + `password='a b\'c\\' options='-c timezone=UTC' Search_Path='public, other'`},
		{"keyword escaped whitespace", keyword + `password=a\ b application_name=one\ two search_path=old`},
		{"keyword trailing escape", keyword + `password=secret\`},
		{"keyword trailing escaped backslashes", keyword + `password=secret\\`},
		{"keyword empty final value", keyword + `password=   `},
		{"keyword empty trailing escape", keyword + `password=\`},
		{"keyword repeated case variants", keyword + `Search_Path=public search_path='other' SEARCH_PATH=last`},
		{"keyword quoted search path in value", keyword + `application_name='search_path=public'`},
		{"keyword database aliases", keyword + `dbname=first database=second dbname=last`},
		{"keyword adjacent quoted values", keyword + `password='first'password='last'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, err := pgx.ParseConfig(tt.dsn)
			if err != nil {
				t.Fatal("invalid regression fixture DSN")
			}
			scoped, err := jobTimeSchemaDSN(tt.dsn, schema)
			if err != nil {
				t.Fatal(err)
			}
			after, err := pgx.ParseConfig(scoped)
			if err != nil {
				t.Fatal("rewritten fixture DSN cannot be parsed")
			}
			if after.RuntimeParams["search_path"] != schema {
				t.Fatal("rewritten DSN did not isolate the fixture schema")
			}
			for key := range before.RuntimeParams {
				if strings.EqualFold(key, "search_path") {
					delete(before.RuntimeParams, key)
				}
			}
			delete(after.RuntimeParams, "search_path")
			if !reflect.DeepEqual(before.RuntimeParams, after.RuntimeParams) || before.Password != after.Password || before.User != after.User || before.Database != after.Database || before.Host != after.Host || before.Port != after.Port {
				t.Fatal("rewriting the schema changed unrelated connection options")
			}
		})
	}
}
