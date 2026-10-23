package catalog

import (
	"testing"

	"entgo.io/ent/dialect/sql/schema"
	"entgo.io/ent/schema/field"
)

func TestPlanSchemaSignatureBindsAllocationAndOwnershipContracts(t *testing.T) {
	column := &schema.Column{Name: "identity", Type: field.TypeString, Size: 255, Enums: []string{"local", "s3"}}
	reference := &schema.Table{Name: "owners", Columns: []*schema.Column{{Name: "id", Type: field.TypeInt}}}
	fk := &schema.ForeignKey{Symbol: "owner", Columns: []*schema.Column{column}, RefTable: reference, RefColumns: reference.Columns, OnDelete: schema.Restrict, OnUpdate: schema.NoAction}
	table := &schema.Table{Name: "locations", Columns: []*schema.Column{column}, PrimaryKey: []*schema.Column{column}, ForeignKeys: []*schema.ForeignKey{fk}}
	before := Signature([]*schema.Table{table})
	for _, edit := range []func(){
		func() { column.Enums = []string{"local", "s3", "unknown"} },
		func() { column.Size = 128 },
		func() { fk.OnUpdate = schema.Cascade },
		func() { table.PrimaryKey = nil },
	} {
		edit()
		if Signature([]*schema.Table{table}) == before {
			t.Fatal("schema change did not invalidate the plan signature")
		}
		column.Enums, column.Size, fk.OnUpdate, table.PrimaryKey = []string{"local", "s3"}, 255, schema.NoAction, []*schema.Column{column}
	}
}
