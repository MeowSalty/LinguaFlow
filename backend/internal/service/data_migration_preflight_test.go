package service

import (
	"database/sql"
	"testing"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
)

func TestDataVersionPreflightSupportsStartsAndRejectsUnknownWithoutWrites(t *testing.T) {
	for _, test := range []struct {
		name, setup string
		accepted    bool
	}{
		{"new", "", true},
		{"uninitialized", "CREATE TABLE instance_initializations(id INTEGER,version INTEGER,data_version INTEGER)", true},
		{"zero", "CREATE TABLE instance_initializations(id INTEGER,version INTEGER,data_version INTEGER);INSERT INTO instance_initializations VALUES(1,1,0)", true},
		{"current", "CREATE TABLE instance_initializations(id INTEGER,version INTEGER,data_version INTEGER);INSERT INTO instance_initializations VALUES(1,1,3)", true},
		{"future", "CREATE TABLE instance_initializations(id INTEGER,version INTEGER,data_version INTEGER);INSERT INTO instance_initializations VALUES(1,1,4)", false},
		{"negative", "CREATE TABLE instance_initializations(id INTEGER,version INTEGER,data_version INTEGER);INSERT INTO instance_initializations VALUES(1,1,-1)", false},
		{"duplicate", "CREATE TABLE instance_initializations(id INTEGER,version INTEGER,data_version INTEGER);INSERT INTO instance_initializations VALUES(1,1,3),(1,1,3)", false},
		{"bad_singleton", "CREATE TABLE instance_initializations(id INTEGER,version INTEGER,data_version INTEGER);INSERT INTO instance_initializations VALUES(2,1,3)", false},
		{"invalid_value", "CREATE TABLE instance_initializations(id INTEGER,version INTEGER,data_version TEXT);INSERT INTO instance_initializations VALUES(1,1,'invalid')", false},
		{"view", "CREATE VIEW instance_initializations AS SELECT 1 AS id,1 AS version,3 AS data_version", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			defer db.Close()
			if test.setup != "" {
				if _, err := db.Exec(test.setup); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec("PRAGMA query_only=ON"); err != nil {
				t.Fatal(err)
			}
			client := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB("sqlite3", db))))
			err = PreflightDataVersion(t.Context(), client)
			if (err == nil) != test.accepted {
				t.Fatalf("accepted=%v error=%v", test.accepted, err)
			}
		})
	}
}
