package templates

// DefaultServerConfigYAML is a deployment template, not a translation document.
func DefaultServerConfigYAML() []byte {
	data, err := builtinFS.ReadFile("default/server.yaml")
	if err != nil {
		panic("embedded server.yaml missing")
	}
	return data
}
