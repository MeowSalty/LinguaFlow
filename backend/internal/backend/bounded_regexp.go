package backend

import (
	"regexp"
	"regexp/syntax"
	"strings"
	"sync"
	"sync/atomic"
)

var fixedRegexps sync.Map
var fixedRegexpInstructions atomic.Int64
var fixedRegexpCaptures atomic.Int64
var fixedRegexpSealed atomic.Bool

// RegisterFixedRegexp is used only by package initializers for literal rules.
// The accepted finite registry makes NFA metadata a provable fixed H term.
func RegisterFixedRegexp(re *regexp.Regexp) {
	if fixedRegexpSealed.Load() {
		panic("fixed regexp registration after first build")
	}
	if len(re.String()) > 1024 || re.NumSubexp() > 63 {
		panic("fixed regexp exceeds structural bounds")
	}
	ast, e := syntax.Parse(re.String(), syntax.Perl)
	if e != nil {
		panic(e)
	}
	prog, e := syntax.Compile(ast.Simplify())
	if e != nil {
		panic(e)
	}
	if len(prog.Inst) > 512 {
		panic("fixed regexp exceeds instruction bounds")
	}
	fixedRegexps.Store(re, len(prog.Inst))
	for old := fixedRegexpInstructions.Load(); int64(len(prog.Inst)) > old; old = fixedRegexpInstructions.Load() {
		if fixedRegexpInstructions.CompareAndSwap(old, int64(len(prog.Inst))) {
			break
		}
	}
	caps := int64(2 * (re.NumSubexp() + 1))
	for old := fixedRegexpCaptures.Load(); caps > old; old = fixedRegexpCaptures.Load() {
		if fixedRegexpCaptures.CompareAndSwap(old, caps) {
			break
		}
	}
}
func reserveRegexpWorkspace(b *BuildBounds) error {
	fixedRegexpSealed.Store(true)
	if b.regexpReserved {
		return nil
	}
	if e := b.ReserveNodes(4*fixedRegexpInstructions.Load()+4*fixedRegexpCaptures.Load()+64, 1024); e != nil {
		return e
	}
	b.regexpReserved = true
	return nil
}

// The RuneReader entry forces regexp's NFA path. Its queues/threads depend
// only on the fixed compiled expression, never on input length; unlike the
// string entry it cannot select the input-sized bit-state/backtracking stack.
// Reader + returned capture slots are the only per-call owned allocations.
func FindRegexpSubmatch(b *BuildBounds, re *regexp.Regexp, s string) ([]int, error) {
	if _, ok := fixedRegexps.Load(re); !ok {
		return nil, &BuildCapacityError{"unregistered bounded regexp"}
	}
	if e := reserveRegexpWorkspace(b); e != nil {
		return nil, e
	}
	if re.NumSubexp() > 63 {
		return nil, &BuildCapacityError{"regexp captures"}
	}
	if e := b.ReserveNodes(3, 1024); e != nil {
		return nil, e
	}
	return re.FindReaderSubmatchIndex(strings.NewReader(s)), nil
}
func FindRegexpIndex(b *BuildBounds, re *regexp.Regexp, s string) ([]int, error) {
	if _, ok := fixedRegexps.Load(re); !ok {
		return nil, &BuildCapacityError{"unregistered bounded regexp"}
	}
	if e := reserveRegexpWorkspace(b); e != nil {
		return nil, e
	}
	if e := b.ReserveNodes(3, 1024); e != nil {
		return nil, e
	}
	return re.FindReaderIndex(strings.NewReader(s)), nil
}
