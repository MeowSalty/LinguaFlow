package v015

import (
	"strings"
	"testing"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/MeowSalty/LinguaFlow/backend/internal/database"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/authconnection"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent/externalidentity"
	"github.com/MeowSalty/LinguaFlow/backend/internal/migration/operation"
)

func TestPublishedProjectionMissingReceiptNeverEndorsesTarget(t *testing.T) {
	ctx := t.Context()
	db, _, _ := releaseFixture(t, "serve")
	p := fixturePlan(t, db, "serve")
	if err := operation.Initialize(ctx, db, "sqlite", p); err != nil {
		t.Fatal(err)
	}
	if err := ConvertSchema(ctx, db, p); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE linguaflow_migration_steps SET result_digest=NULL WHERE operation_id=? AND step='schema'", p.ID); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{CheckProgress(ctx, db, p, []string{"schema"}), ConvertSchema(ctx, db, p)} {
		if err == nil || !strings.Contains(err.Error(), "exact released tool") {
			t.Fatal("missing projection was silently trusted or lacked recovery guidance", err)
		}
	}
	var missing bool
	if err := db.QueryRowContext(ctx, "SELECT result_digest IS NULL FROM linguaflow_migration_steps WHERE operation_id=? AND step='schema'", p.ID).Scan(&missing); err != nil || !missing {
		t.Fatal("target was re-endorsed after receipt disappeared", err)
	}
}

func TestPublishedProjectionIgnoresLaterNullEvidenceBinding(t *testing.T) {
	ctx := t.Context()
	db, _, _ := releaseFixture(t, "serve")
	p := fixturePlan(t, db, "serve")
	if err := operation.Initialize(ctx, db, "sqlite", p); err != nil {
		t.Fatal(err)
	}
	if err := ConvertSchema(ctx, db, p); err != nil {
		t.Fatal(err)
	}
	c := ent.NewClient(ent.Driver(database.NewDriver(entsql.OpenDB("sqlite3", db))))
	c.AuthConnection.Create().SetID("later-null").SetKind(authconnection.KindOidc).SetState(authconnection.StateDisabled).SaveX(ctx)
	identity := c.ExternalIdentity.Create().SetUserID(1).SetProtocol(externalidentity.ProtocolOidc).SetAuthority("later-null").SetSubject("fixture").SaveX(ctx)
	c.LoginBinding.Create().SetUserID(1).SetConnectionID("later-null").SetExternalIdentityID(identity.ID).SaveX(ctx)
	if err := verifyPublishedProjection(ctx, db, p); err != nil {
		t.Fatal("projection included a binding outside the original conversion scope", err)
	}
}
