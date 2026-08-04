package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"
)

func main() {
	in := flag.String("in", "", "input OpenAPI root spec file")
	out := flag.String("out", "", "output bundled OpenAPI file")
	flag.Parse()

	if *in == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: openapi-bundle -in <spec.yaml> -out <bundled.yaml>")
		os.Exit(2)
	}

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true

	doc, err := loader.LoadFromFile(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load openapi spec: %v\n", err)
		os.Exit(1)
	}

	doc.InternalizeRefs(context.Background(), nil)

	if err := doc.Validate(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "validate bundled openapi spec: %v\n", err)
		os.Exit(1)
	}

	data, err := yaml.Marshal(doc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal bundled openapi spec: %v\n", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create output directory: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(*out, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write bundled openapi spec: %v\n", err)
		os.Exit(1)
	}
}
