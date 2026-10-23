package authrules

func derivedDefinitionMap() map[string]derivedDefinition {
	out := make(map[string]derivedDefinition, len(derivedDefinitions))
	for _, definition := range derivedDefinitions {
		out[definition.FieldID] = definition
	}
	return out
}
