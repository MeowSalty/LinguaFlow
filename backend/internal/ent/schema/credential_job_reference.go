package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// A reference is retained for every extant job, including completed jobs.
type CredentialJobReference struct{ ent.Schema }

func (CredentialJobReference) Fields() []ent.Field {
	return []ent.Field{
		field.Int("job_id").Positive().Immutable(),
		field.Int("credential_version_id").Positive().Immutable(),
	}
}
func (CredentialJobReference) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("job", Job.Type).Ref("credential_references").Field("job_id").Unique().Required().Immutable(),
		edge.From("credential_version", CredentialVersion.Type).Ref("job_references").Field("credential_version_id").Unique().Required().Immutable(),
	}
}
func (CredentialJobReference) Indexes() []ent.Index {
	return []ent.Index{index.Fields("job_id", "credential_version_id").Unique()}
}
