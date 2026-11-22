// ci-inventory preserves go list's selected package inventory and adds evidence
// for packages whose selected production sources have no Go function bodies.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type packageRecord struct {
	ImportPath   string
	Dir          string
	GoFiles      []string
	CgoFiles     []string
	TestGoFiles  []string
	XTestGoFiles []string
	Error        json.RawMessage
	Incomplete   bool
}

type sourceFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

type sourceEvidence struct {
	Kind   string       `json:"kind"`
	Parser string       `json:"parser"`
	Files  []sourceFile `json:"files"`
}

func hasGoFunctionBody(name string, source []byte) (bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), name, source, parser.AllErrors|parser.SkipObjectResolution)
	if err != nil {
		return false, fmt.Errorf("parse selected Go source %s: %w", name, err)
	}
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		if found {
			return false
		}
		switch node := node.(type) {
		case *ast.FuncDecl:
			found = node.Body != nil
		case *ast.FuncLit:
			found = true
		}
		return !found
	})
	return found, nil
}

func noFunctionSourceEvidence(record packageRecord) (*sourceEvidence, error) {
	if len(record.TestGoFiles)+len(record.XTestGoFiles) != 0 {
		return nil, nil
	}
	names := append(append([]string(nil), record.GoFiles...), record.CgoFiles...)
	if record.Dir == "" || len(names) == 0 {
		return nil, nil
	}
	evidence := &sourceEvidence{Kind: "no-go-function-bodies", Parser: "go/parser"}
	seen := make(map[string]bool, len(names))
	hasBody := false
	for _, name := range names {
		if filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) || !strings.HasSuffix(name, ".go") || seen[name] {
			return nil, fmt.Errorf("invalid selected Go source %q", name)
		}
		seen[name] = true
		source, err := os.ReadFile(filepath.Join(record.Dir, name))
		if err != nil {
			return nil, fmt.Errorf("read selected Go source %s: %w", name, err)
		}
		body, err := hasGoFunctionBody(name, source)
		if err != nil {
			return nil, err
		}
		hasBody = hasBody || body
		digest := sha256.Sum256(source)
		evidence.Files = append(evidence.Files, sourceFile{Name: name, SHA256: hex.EncodeToString(digest[:])})
	}
	if hasBody {
		return nil, nil
	}
	return evidence, nil
}

func annotateInventory(data []byte) ([]map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var records []map[string]json.RawMessage
	seen := make(map[string]bool)
	for {
		var raw map[string]json.RawMessage
		if err := decoder.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode go list inventory: %w", err)
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("encode package record: %w", err)
		}
		var record packageRecord
		if err := json.Unmarshal(encoded, &record); err != nil {
			return nil, fmt.Errorf("decode package record: %w", err)
		}
		if record.ImportPath == "" || record.Incomplete || len(record.Error) != 0 && string(record.Error) != "null" || seen[record.ImportPath] {
			return nil, fmt.Errorf("invalid or repeated package inventory: %s", record.ImportPath)
		}
		seen[record.ImportPath] = true
		evidence, err := noFunctionSourceEvidence(record)
		if err != nil {
			return nil, fmt.Errorf("package %s: %w", record.ImportPath, err)
		}
		raw["NoFunctionSourceEvidence"], err = json.Marshal(evidence)
		if err != nil {
			return nil, fmt.Errorf("encode source evidence: %w", err)
		}
		records = append(records, raw)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("go list returned an empty inventory")
	}
	return records, nil
}

func run(args []string, output io.Writer) error {
	listArgs := append([]string{"list", "-json"}, args...)
	listArgs = append(listArgs, "./...")
	command := exec.Command("go", listArgs...)
	command.Stderr = os.Stderr
	data, err := command.Output()
	if err != nil {
		return fmt.Errorf("discover complete package inventory: %w", err)
	}
	records, err := annotateInventory(data)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			return fmt.Errorf("write package inventory: %w", err)
		}
	}
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
