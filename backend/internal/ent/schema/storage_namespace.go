package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// StorageNamespace is the durable allocation gate for overlapping S3 prefixes.
// Bucket names intentionally share a gate across endpoint aliases.
type StorageNamespace struct{ ent.Schema }

func (StorageNamespace) Fields() []ent.Field {
	return []ent.Field{field.String("bucket").NotEmpty().Unique().Immutable()}
}
