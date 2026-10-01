package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validRevisionInput = `schema_version: 1
segments:
  - index: 0
    source: hello world
    target: 已有译文 ${LITERAL}
    issues:
      - code: mistranslation
        message: The translation has the wrong meaning.
`

func TestRevisionInputStrictDataContract(t *testing.T) {
	for name, document := range map[string]string{
		"missing version":     strings.Replace(validRevisionInput, "schema_version: 1\n", "", 1),
		"unsupported version": strings.Replace(validRevisionInput, "schema_version: 1", "schema_version: 2", 1),
		"empty segments":      "schema_version: 1\nsegments: []\n",
		"missing index":       strings.Replace(validRevisionInput, "- index: 0\n", "-\n", 1),
		"null":                strings.Replace(validRevisionInput, "index: 0", "index: null", 1),
		"wrong type":          strings.Replace(validRevisionInput, "index: 0", "index: '0'", 1),
		"unknown field":       validRevisionInput + "secret: private\n",
		"duplicate":           validRevisionInput + "schema_version: 1\n",
		"missing target":      strings.Replace(validRevisionInput, "    target: 已有译文 ${LITERAL}\n", "", 1),
		"invalid code":        strings.Replace(validRevisionInput, "mistranslation", "not-a-code", 1),
		"empty message":       strings.Replace(validRevisionInput, "The translation has the wrong meaning.", "''", 1),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "review.yaml")
			if err := os.WriteFile(path, []byte(document), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadCLIRevisionInput(path); err == nil {
				t.Fatal("invalid revision input accepted")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "review.yaml")
	if err := os.WriteFile(path, []byte(validRevisionInput), 0600); err != nil {
		t.Fatal(err)
	}
	input, err := LoadCLIRevisionInput(path)
	if err != nil {
		t.Fatal(err)
	}
	if input.Segments[0].Target != "已有译文 ${LITERAL}" {
		t.Fatal("revision body was expanded")
	}
}
