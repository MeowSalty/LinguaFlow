package prompt

import "github.com/MeowSalty/LinguaFlow/backend/internal/backend"

// Fixed schemas belong to the package. Each builder receives an independently
// metered clone, preserving callers' ability to edit request-specific fields.
var bootstrapSchemaSnapshot = BootstrapSchema()
var semanticSchemaSnapshot = SemanticQAIssueSchema()
var adjudicationSchemaSnapshot = AdjudicationVerdictSchema()
var reviseSchemaSnapshot = ReviseRevisionSchema(false, nil)
var rubyItemSchemaSnapshot = RubyOutputSchema([]string{"item"})["properties"].(map[string]any)["item"].(map[string]any)
var rubyItemV2SchemaSnapshot = RubyOutputSchema([]string{"item"}, 2)["properties"].(map[string]any)["item"].(map[string]any)

func cloneSchemaBounded(b *backend.BuildBounds, source map[string]any) (map[string]any, error) {
	var out map[string]any
	err := b.CloneInto(source, &out)
	return out, err
}
func BootstrapSchemaBounded(b *backend.BuildBounds) (map[string]any, error) {
	return cloneSchemaBounded(b, bootstrapSchemaSnapshot)
}
func SemanticQAIssueSchemaBounded(b *backend.BuildBounds) (map[string]any, error) {
	return cloneSchemaBounded(b, semanticSchemaSnapshot)
}
func AdjudicationVerdictSchemaBounded(b *backend.BuildBounds) (map[string]any, error) {
	return cloneSchemaBounded(b, adjudicationSchemaSnapshot)
}
func RubyOutputSchemaBounded(b *backend.BuildBounds, ids []string, protocol ...int) (map[string]any, error) {
	source := rubyItemSchemaSnapshot
	if len(protocol) > 0 && protocol[0] >= 2 {
		source = rubyItemV2SchemaSnapshot
	}
	props, err := backend.MakeMap[string, any](b, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		item, err := cloneSchemaBounded(b, source)
		if err != nil {
			return nil, err
		}
		props[id] = item
	}
	return map[string]any{"type": "object", "properties": props}, nil
}
func ReviseRevisionSchemaBounded(b *backend.BuildBounds, includeRuby bool, ids []string, protocol ...int) (map[string]any, error) {
	out, err := cloneSchemaBounded(b, reviseSchemaSnapshot)
	if err != nil || !includeRuby {
		return out, err
	}
	rubySchema, err := RubyOutputSchemaBounded(b, ids, protocol...)
	if err != nil {
		return nil, err
	}
	props := out["properties"].(map[string]any)
	props["ruby_output"] = rubySchema
	required, err := backend.AppendNode(b, out["required"].([]string), "ruby_output")
	if err != nil {
		return nil, err
	}
	out["required"] = required
	return out, nil
}
