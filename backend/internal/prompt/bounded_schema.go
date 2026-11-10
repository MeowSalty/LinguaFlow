package prompt

import "github.com/MeowSalty/LinguaFlow/backend/internal/backend"

// Fixed schemas contain <64 maps/groups, boxed scalars and slice headers;
// each record/group is <=1024 bytes. Ruby's per-ID shape has seven maps,
// 16 property slots, two boxed slices and one parent-map slot: 32 E credits.
func BootstrapSchemaBounded(b *backend.BuildBounds) (map[string]any, error) {
	if e := b.ReserveNodes(64, 1024); e != nil {
		return nil, e
	}
	return BootstrapSchema(), nil
}
func SemanticQAIssueSchemaBounded(b *backend.BuildBounds) (map[string]any, error) {
	if e := b.ReserveNodes(64, 1024); e != nil {
		return nil, e
	}
	return SemanticQAIssueSchema(), nil
}
func AdjudicationVerdictSchemaBounded(b *backend.BuildBounds) (map[string]any, error) {
	if e := b.ReserveNodes(64, 1024); e != nil {
		return nil, e
	}
	return AdjudicationVerdictSchema(), nil
}
func RubyOutputSchemaBounded(b *backend.BuildBounds, ids []string, protocol ...int) (map[string]any, error) {
	if e := b.ReserveNodes(32*int64(len(ids))+16, 1024); e != nil {
		return nil, e
	}
	return RubyOutputSchema(ids, protocol...), nil
}
func ReviseRevisionSchemaBounded(b *backend.BuildBounds, includeRuby bool, ids []string, protocol ...int) (map[string]any, error) {
	n := int64(64)
	if includeRuby {
		n += 32 * int64(len(ids))
	}
	if e := b.ReserveNodes(n, 1024); e != nil {
		return nil, e
	}
	return ReviseRevisionSchema(includeRuby, ids, protocol...), nil
}
