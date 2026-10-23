package v015

import (
	"errors"
	"strings"
	"unicode"
)

// CHECK expressions come from the fixed table DDL or PostgreSQL's constraint
// decompiler. The finite parser preserves boolean grouping and literal values;
// it normalizes only the supported BETWEEN, IN and PostgreSQL cast rewrites.
func canonicalCheck(expression, driver string) (string, error) {
	tokens, err := checkTokens(expression)
	if err != nil {
		return "", err
	}
	p := checkParser{tokens: tokens, postgres: driver == "postgres"}
	node := p.or()
	if p.err != nil || p.pos != len(tokens) || node == nil {
		return "", errors.New("CHECK expression is outside the fixed grammar")
	}
	return node.canonical(), nil
}

func identifierByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

func quotedEnd(sql string, start int) int {
	quote := sql[start]
	if quote == '[' {
		quote = ']'
	}
	for i := start + 1; i < len(sql); i++ {
		if sql[i] != quote {
			continue
		}
		if i+1 < len(sql) && sql[i+1] == quote {
			i++
			continue
		}
		return i + 1
	}
	return len(sql)
}

func sqliteChecks(ddl string) ([]string, error) {
	var checks []string
	for i := 0; i < len(ddl); {
		if strings.ContainsRune("'\"`[", rune(ddl[i])) {
			i = quotedEnd(ddl, i)
			continue
		}
		if !identifierByte(ddl[i]) {
			i++
			continue
		}
		start := i
		for i < len(ddl) && identifierByte(ddl[i]) {
			i++
		}
		if !strings.EqualFold(ddl[start:i], "CHECK") {
			continue
		}
		for i < len(ddl) && unicode.IsSpace(rune(ddl[i])) {
			i++
		}
		if i == len(ddl) || ddl[i] != '(' {
			return nil, errors.New("unrecognized SQLite CHECK clause")
		}
		i++
		start, depth := i, 1
		for i < len(ddl) && depth > 0 {
			if strings.ContainsRune("'\"`[", rune(ddl[i])) {
				i = quotedEnd(ddl, i)
				continue
			}
			switch ddl[i] {
			case '(':
				depth++
			case ')':
				depth--
			}
			i++
		}
		if depth != 0 {
			return nil, errors.New("unclosed SQLite CHECK clause")
		}
		checks = append(checks, ddl[start:i-1])
	}
	return checks, nil
}
