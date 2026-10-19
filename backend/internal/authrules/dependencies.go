package authrules

import "sort"

func definitionDependencies(definition derivedDefinition) []string {
	deps := append([]string{}, definition.DependsOn...)
	deps = append(deps, definition.TrustDeps...)
	return uniqueSorted(deps)
}

func fieldDependencies(f Field) []string {
	deps := append([]string{}, f.DerivedFrom...)
	if f.TrustPair != nil && f.TrustPair.PeerFieldID != "" {
		deps = append(deps, f.TrustPair.PeerFieldID)
	}
	return uniqueSorted(deps)
}

func requiredConditionIDs(rules ...RuleSet) []string {
	ids := []string{}
	for _, rule := range rules {
		for _, condition := range rule.Conditions {
			ids = append(ids, condition.FieldID)
		}
	}
	return ids
}

// walkCatalogDependencies expands every program-defined derived and trust-pair
// edge from the same catalog that extraction and evaluation consume.
func walkCatalogDependencies(catalog map[string]Field, ids ...string) []string {
	seen := map[string]bool{}
	queue := append([]string{}, ids...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if seen[id] {
			continue
		}
		seen[id] = true
		f, ok := catalog[id]
		if !ok {
			continue
		}
		queue = append(queue, fieldDependencies(f)...)
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
