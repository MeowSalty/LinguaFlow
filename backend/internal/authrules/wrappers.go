package authrules

func Catalog(protocol string, mappings []Field) ([]Field, error) {
	return CatalogForPurpose(protocol, mappings, purposeSignup)
}

func Validate(protocol string, mappings []Field, rules ...RuleSet) error {
	return ValidateForPurpose(protocol, mappings, purposeSignup, rules...)
}

func ValidateScopes(protocol string, mappings []Field, scopes []string, userinfoEnabled bool, rules ...RuleSet) error {
	return ValidateScopesForPurpose(protocol, mappings, scopes, userinfoEnabled, purposeSignup, rules...)
}
