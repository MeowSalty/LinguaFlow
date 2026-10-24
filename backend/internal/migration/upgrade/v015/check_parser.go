package v015

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type checkNode struct {
	op    string
	value string
	args  []*checkNode
}

func (n *checkNode) canonical() string {
	if n == nil {
		return "invalid"
	}
	if len(n.args) == 0 {
		value, _ := json.Marshal(n.value)
		return n.op + ":" + string(value)
	}
	args := make([]string, 0, len(n.args))
	var appendNode func(*checkNode)
	appendNode = func(child *checkNode) {
		if child != nil && child.op == n.op && (n.op == "and" || n.op == "or") {
			for _, item := range child.args {
				appendNode(item)
			}
			return
		}
		args = append(args, child.canonical())
	}
	for _, arg := range n.args {
		appendNode(arg)
	}
	if n.op == "and" || n.op == "or" || n.op == "array" {
		sort.Strings(args)
	}
	return n.op + "(" + strings.Join(args, ",") + ")"
}

func checkTokens(sql string) ([]string, error) {
	var result []string
	for i := 0; i < len(sql); {
		if unicode.IsSpace(rune(sql[i])) {
			i++
			continue
		}
		start := i
		if sql[i] == '\'' || sql[i] == '"' || sql[i] == '`' {
			i = quotedEnd(sql, i)
			if i <= start+1 || sql[i-1] != sql[start] {
				return nil, errors.New("unclosed CHECK quote")
			}
			result = append(result, sql[start:i])
			continue
		}
		if identifierByte(sql[i]) {
			for i < len(sql) && identifierByte(sql[i]) {
				i++
			}
			result = append(result, strings.ToLower(sql[start:i]))
			continue
		}
		i++
		if i < len(sql) && (sql[start:i+1] == "::" || sql[start:i+1] == ">=" || sql[start:i+1] == "<=" || sql[start:i+1] == "<>") {
			i++
		}
		token := sql[start:i]
		if !strings.Contains("()[],+=<>-:", token[:1]) {
			return nil, errors.New("unknown CHECK token")
		}
		result = append(result, token)
	}
	return result, nil
}

type checkParser struct {
	tokens   []string
	pos      int
	err      error
	postgres bool
}

func (p *checkParser) take(token string) bool {
	if p.pos < len(p.tokens) && p.tokens[p.pos] == token {
		p.pos++
		return true
	}
	return false
}

func (p *checkParser) require(token string) {
	if !p.take(token) {
		p.err = errors.New("unexpected CHECK token")
	}
}

func (p *checkParser) or() *checkNode {
	node := p.and()
	for p.take("or") {
		node = &checkNode{op: "or", args: []*checkNode{node, p.and()}}
	}
	return node
}

func (p *checkParser) and() *checkNode {
	node := p.predicate()
	for p.take("and") {
		node = &checkNode{op: "and", args: []*checkNode{node, p.predicate()}}
	}
	return node
}

func (p *checkParser) predicate() *checkNode {
	left := p.sum()
	if p.take("is") {
		op := "is_null"
		if p.take("not") {
			op = "is_not_null"
		}
		p.require("null")
		return &checkNode{op: op, args: []*checkNode{left}}
	}
	if p.take("between") {
		minimum := p.sum()
		p.require("and")
		maximum := p.sum()
		return &checkNode{op: "and", args: []*checkNode{{op: ">=", args: []*checkNode{left, minimum}}, {op: "<=", args: []*checkNode{left, maximum}}}}
	}
	if p.take("in") {
		p.require("(")
		values := &checkNode{op: "array", args: []*checkNode{p.sum()}}
		for p.take(",") {
			values.args = append(values.args, p.sum())
		}
		p.require(")")
		return &checkNode{op: "in", args: []*checkNode{left, values}}
	}
	for _, op := range []string{"=", "<>", ">=", "<=", ">", "<"} {
		if !p.take(op) {
			continue
		}
		if op == "=" && p.postgres && p.take("any") {
			p.require("(")
			values := p.sum()
			p.require(")")
			if values == nil || values.op != "array" {
				p.err = errors.New("CHECK ANY requires a fixed array")
			}
			return &checkNode{op: "in", args: []*checkNode{left, values}}
		}
		return &checkNode{op: op, args: []*checkNode{left, p.sum()}}
	}
	return left
}

func (p *checkParser) sum() *checkNode {
	node := p.atom()
	for p.take("+") {
		node = &checkNode{op: "+", args: []*checkNode{node, p.atom()}}
	}
	return node
}

func (p *checkParser) atom() *checkNode {
	var node *checkNode
	switch {
	case p.take("("):
		node = p.or()
		p.require(")")
	case p.postgres && p.take("array"):
		p.require("[")
		node = &checkNode{op: "array", args: []*checkNode{p.sum()}}
		for p.take(",") {
			node.args = append(node.args, p.sum())
		}
		p.require("]")
	case p.pos < len(p.tokens):
		negative := p.take("-")
		if p.pos >= len(p.tokens) {
			p.err = errors.New("missing CHECK operand")
			return nil
		}
		token := p.tokens[p.pos]
		p.pos++
		node = &checkNode{op: "id", value: token}
		if token[0] == '\'' {
			node.op, node.value = "string", strings.ReplaceAll(token[1:len(token)-1], "''", "'")
		} else if token[0] == '"' || token[0] == '`' {
			node.value = token[1 : len(token)-1]
		} else if integer, err := strconv.ParseInt(token, 10, 64); err == nil {
			if negative {
				integer = -integer
			}
			node.op, node.value = "integer", strconv.FormatInt(integer, 10)
		} else if negative || !identifierByte(token[0]) {
			p.err = errors.New("unknown CHECK operand")
		}
	default:
		p.err = errors.New("missing CHECK operand")
		return nil
	}
	if node == nil {
		p.err = errors.New("missing CHECK operand")
		return nil
	}
	for p.postgres && p.take("::") {
		switch {
		case p.take("text"), p.take("character") && p.take("varying"):
			if p.take("[") {
				p.require("]")
				if node.op != "array" {
					p.err = errors.New("CHECK cast requires a fixed array")
				}
			}
		case p.take("bigint"), p.take("integer"), p.take("smallint"):
			if node.op == "integer" {
				continue
			}
			if node.op != "string" {
				p.err = errors.New("CHECK integer cast requires a fixed integer literal")
				break
			}
			integer, err := strconv.ParseInt(node.value, 10, 64)
			if err != nil {
				p.err = err
			} else {
				node.op, node.value = "integer", strconv.FormatInt(integer, 10)
			}
		default:
			p.err = errors.New("unknown CHECK cast")
			return node
		}
	}
	return node
}
