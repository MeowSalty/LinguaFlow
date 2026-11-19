package repair

import (
	"errors"
	responsecontract "github.com/MeowSalty/LinguaFlow/backend/internal/execution/response"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
)

// ArchiveMember is one normalized output, bound to the frozen request ordinal.
// Failure is a content failure; capacity and cancellation remain returned errors.
type ArchiveMember struct {
	Outcome    responsecontract.MemberOutcome
	Target     string
	Revision   *prompt.ReviseRevision
	RubyOutput []ruby.OutputEntry
	Missing    bool
	Failure    string
}

// ArchiveContentError is a model syntax/schema failure. It never wraps a
// capacity, cancellation, identity, or storage failure.
type ArchiveContentError struct{ Err error }

func (e *ArchiveContentError) Error() string { return e.Err.Error() }
func (e *ArchiveContentError) Unwrap() error { return e.Err }

type archiveSlot struct {
	target        backend.JSONValue
	ruby          backend.JSONValue
	publicRuby    backend.JSONValue
	parts         []string
	rubyLines     []string
	found         bool
	rubySeen      bool
	rubyDuplicate bool
}

// ArchiveWork counts bytes presented to structural scanners and decoders, not
// heap estimates. JSONScanBytes includes both passes of a successful JSONView.
type ArchiveWork struct {
	SourceBytes    int64
	JSONScanBytes  int64
	JSONScans      int64
	SelectionBytes int64
	IDDecodeBytes  int64
}

// Archive owns a bounded preparation view and ordinal selection table. It does
// not retain materialized private targets. Its bounds belong to the caller;
// Release clears borrowed references before that caller retires the owner.
type Archive struct {
	ids           []string
	slots         []archiveSlot
	glossary      backend.JSONValue
	glossaryLines []string
	repaired      []string
	textMode      bool
	revise        bool
	work          ArchiveWork
}

func (a *Archive) Work() ArchiveWork  { return a.work }
func (a *Archive) Repaired() []string { return a.repaired }
func (a *Archive) Release() {
	if a == nil {
		return
	}
	a.ids = nil
	a.slots = nil
	a.glossary = backend.JSONValue{}
	a.glossaryLines = nil
	a.repaired = nil
}

func PrepareArchive(b *backend.BuildBounds, text string, textMode, revise bool, ids []string, opt Options) (*Archive, error) {
	if err := b.CheckContext(); err != nil {
		return nil, err
	}
	if err := backend.ReserveSlice[Archive](b, 1); err != nil {
		return nil, err
	}
	if err := backend.ReserveSlice[string](b, int64(len(ids))); err != nil {
		return nil, err
	}
	if err := backend.ReserveSlice[archiveSlot](b, int64(len(ids))); err != nil {
		return nil, err
	}
	a := &Archive{ids: make([]string, len(ids)), slots: make([]archiveSlot, len(ids)), revise: revise, work: ArchiveWork{SourceBytes: int64(len(text))}}
	copy(a.ids, ids)
	index := make(map[string]int, len(ids)) // Go map internals are excluded from exact backing accounting.
	for ordinal, id := range ids {
		if _, exists := index[id]; exists || id == "" {
			a.Release()
			return nil, errors.New("invalid archive prompt identity")
		}
		index[id] = ordinal
	}
	opt.Bounds = b
	if textMode {
		recognized, err := a.prepareText(b, text, index, opt)
		if err != nil {
			a.Release()
			return nil, err
		}
		if recognized {
			a.textMode = true
			return a, nil
		}
		clear(a.slots)
		a.glossaryLines = nil
	}
	v, err := a.prepareView(b, text, opt)
	if err == nil {
		err = a.selectJSON(b, v, index, opt)
	}
	if err != nil {
		a.Release()
		var syntax *backend.JSONSyntaxError
		if errors.As(err, &syntax) {
			return nil, &ArchiveContentError{Err: err}
		}
		return nil, err
	}
	return a, nil
}

func (a *Archive) jsonView(b *backend.BuildBounds, text string) (backend.JSONValue, error) {
	data, err := copyBytesBounded(b, text)
	if err != nil {
		return backend.JSONValue{}, err
	}
	a.work.JSONScanBytes += int64(len(data))
	a.work.JSONScans++
	v, err := backend.NewJSONView(b, data)
	if err == nil {
		a.work.JSONScanBytes += int64(len(data))
		a.work.JSONScans++
	}
	return v, err
}

func (a *Archive) prepareView(b *backend.BuildBounds, text string, opt Options) (backend.JSONValue, error) {
	// The existing structural repair chain is bounded by four operators and one
	// salvage recursion. It runs once per attempt, independently of member count.
	if err := jsonDepthBounded(text); err != nil {
		return backend.JSONValue{}, err
	}
	if opt.JSONStructural {
		cleaned, did, err := cleanZeroWidthBounded(b, text)
		if err != nil {
			return backend.JSONValue{}, err
		}
		if did {
			a.repaired, err = backend.AppendNode(b, a.repaired, "json.strip-bom-zw")
			if err != nil {
				return backend.JSONValue{}, err
			}
		}
		text = cleaned
	}
	if a.revise && opt.JSONStructural {
		bare := strings.TrimSpace(stripCodeFence(text))
		if strings.HasPrefix(bare, "[") {
			v, err := a.firstArray(b, bare)
			if err == nil || errors.Is(err, backend.ErrBuildCapacity) {
				return v, err
			}
		}
	}
	body := pickEnvelopeBody(text, opt)
	v, err := a.jsonView(b, body)
	if err == nil || errors.Is(err, backend.ErrBuildCapacity) {
		return v, err
	}
	if opt.JSONStructural {
		fixed := body
		if fixed == "" {
			fixed = text
		}
		for kind, op := range [4]string{"json.trailing-comma", "json.escape-control", "json.escape-quotes", "json.close-braces"} {
			if kind == 3 && opt.salvageDeclined {
				continue
			}
			s, e := fixJSONBounded(b, fixed, kind)
			if e != nil {
				return v, e
			}
			if s != fixed {
				a.repaired, e = backend.AppendNode(b, a.repaired, op)
				if e != nil {
					return v, e
				}
				fixed = s
			}
		}
		v, err = a.jsonView(b, pickEnvelopeBody(fixed, opt))
		if err == nil || errors.Is(err, backend.ErrBuildCapacity) {
			return v, err
		}
		if cut, ok := salvageCut(text, opt); ok {
			marker := len(a.repaired)
			v, e := a.prepareView(b, cut, opt.enterSalvage())
			if e == nil {
				a.repaired, e = backend.AppendNode(b, a.repaired, "json.truncation-salvage")
				if e == nil {
					copy(a.repaired[marker+1:], a.repaired[marker:len(a.repaired)-1])
					a.repaired[marker] = "json.truncation-salvage"
				}
				return v, e
			}
			if errors.Is(e, backend.ErrBuildCapacity) {
				return v, e
			}
		}
		if a.revise {
			for attempts, i := 0, 0; i < len(text) && attempts < robustScanMaxAttempts; i++ {
				if text[i] != '[' {
					continue
				}
				attempts++
				candidate, e := a.firstArray(b, text[i:])
				if e == nil {
					return candidate, nil
				}
				if errors.Is(e, backend.ErrBuildCapacity) {
					return v, e
				}
			}
		}
	}
	return v, err
}

func (a *Archive) firstArray(b *backend.BuildBounds, text string) (backend.JSONValue, error) {
	data, err := copyBytesBounded(b, text)
	if err != nil {
		return backend.JSONValue{}, err
	}
	a.work.JSONScanBytes += int64(len(data))
	a.work.JSONScans++
	raw, _, err := backend.FirstJSONRaw(b, data)
	if err != nil {
		return backend.JSONValue{}, err
	}
	a.work.JSONScanBytes += int64(len(raw))
	a.work.JSONScans++
	v, err := backend.NewJSONView(b, raw)
	if err == nil {
		a.work.JSONScanBytes += int64(len(raw))
		a.work.JSONScans++
	}
	return v, err
}

func (a *Archive) selectJSON(b *backend.BuildBounds, v backend.JSONValue, index map[string]int, opt Options) error {
	if v.Kind() != '{' && !(a.revise && v.Kind() == '[') {
		return &ArchiveContentError{Err: errors.New("response must be an object")}
	}
	var translations, revisions, output, data backend.JSONValue
	var aliases [4]backend.JSONValue
	if v.Kind() == '{' {
		err := v.EachField(func(key, value backend.JSONValue) error {
			a.work.SelectionBytes += int64(len(key.Raw()))
			switch {
			case key.StringEquals("translations"):
				translations = value
			case key.StringEquals("revisions"):
				revisions = value
			case key.StringEquals("ruby_output"):
				output = value
			case key.StringEquals("glossary"):
				a.glossary = value
			case key.StringEquals("data"):
				data = value
			default:
				for i, name := range [4]string{"translation", "result", "output", "results"} {
					if key.StringEquals(name) {
						aliases[i] = value
					}
				}
			}
			return b.CheckContext()
		})
		if err != nil {
			return err
		}
	}
	rows := translations
	if a.revise {
		rows = revisions
		if v.Kind() == '[' {
			rows = v
		}
		if rows.Kind() != '[' {
			return &ArchiveContentError{Err: errors.New("revisions must be an array")}
		}
	} else {
		if rows.Kind() == 0 && opt.SchemaAliases {
			for _, value := range aliases {
				if value.Kind() == '{' {
					rows = value
					break
				}
			}
			if rows.Kind() == 0 {
				var err error
				rows, err = archiveField(data, "translations")
				if err != nil {
					return err
				}
			}
			if rows.Kind() != 0 {
				var err error
				a.repaired, err = backend.AppendNode(b, a.repaired, "schema.alias")
				if err != nil {
					return err
				}
			}
		}
		if rows.Kind() == 0 {
			return &ArchiveContentError{Err: errors.New("response missing translations field")}
		}
		if rows.Kind() != '{' && rows.Kind() != '[' {
			return &ArchiveContentError{Err: errors.New("expected translations object or array")}
		}
	}
	if rows.Kind() == '{' {
		if err := rows.EachField(func(key, row backend.JSONValue) error {
			ordinal, found, err := a.ordinal(b, key, index, false)
			if err != nil || !found {
				return err
			}
			return a.selectRow(b, ordinal, row)
		}); err != nil {
			return err
		}
	} else {
		if err := rows.EachArray(func(row backend.JSONValue) error {
			if row.Kind() != '{' {
				return nil
			}
			id, err := archiveField(row, "id")
			if err != nil {
				return err
			}
			ordinal, found, err := a.ordinal(b, id, index, a.revise)
			if err != nil || !found {
				return err
			}
			if a.revise && a.slots[ordinal].found {
				return nil
			}
			return a.selectRow(b, ordinal, row)
		}); err != nil {
			return err
		}
	}
	if output.Kind() == '{' {
		return output.EachField(func(key, value backend.JSONValue) error {
			ordinal, found, err := a.ordinal(b, key, index, false)
			if err != nil || !found {
				return err
			}
			s := &a.slots[ordinal]
			if s.rubySeen {
				s.rubyDuplicate = true
				s.publicRuby = backend.JSONValue{}
			} else {
				s.publicRuby = value
			}
			s.rubySeen = true
			return nil
		})
	}
	return nil
}

func (a *Archive) ordinal(b *backend.BuildBounds, key backend.JSONValue, index map[string]int, trim bool) (int, bool, error) {
	if key.Kind() != '"' {
		return 0, false, nil
	}
	a.work.IDDecodeBytes += int64(len(key.Raw()))
	name, err := key.StringLimitedBounded(b, b.OutputLimit())
	if err != nil {
		return 0, false, err
	}
	if trim {
		name = strings.TrimSpace(name)
	}
	ordinal, found := index[name]
	return ordinal, found, nil
}

func (a *Archive) selectRow(b *backend.BuildBounds, ordinal int, row backend.JSONValue) error {
	s := &a.slots[ordinal]
	var values [4]backend.JSONValue
	var localRuby backend.JSONValue
	seen, duplicate := false, false
	if row.Kind() == '{' {
		if err := row.EachField(func(key, value backend.JSONValue) error {
			a.work.SelectionBytes += int64(len(key.Raw()))
			for i, name := range [4]string{"target", "translation", "text", "source"} {
				if key.StringEquals(name) {
					values[i] = value
				}
			}
			if key.StringEquals("ruby_output") {
				if seen {
					duplicate = true
				}
				seen = true
				localRuby = value
			}
			return b.CheckContext()
		}); err != nil {
			return err
		}
	}
	if a.revise {
		if values[0].Kind() != '"' || archiveBlank(values[0].Raw()) {
			return nil
		}
		s.target, s.found = values[0], true
		return nil
	}
	s.found, s.target = true, row
	if row.Kind() == '{' {
		for _, value := range values {
			if value.Kind() == '"' && !value.StringEquals("") {
				s.target = value
				break
			}
		}
	}
	// Duplicate public ruby fields invalidate that public member. Existing
	// semantics then permit its separately unique row-local fallback.
	if seen && !duplicate {
		s.ruby = localRuby
	} else {
		s.ruby = backend.JSONValue{}
	}
	return nil
}

// archiveBlank tests TrimSpace emptiness without materializing all revision
// targets. JSONView has already validated escapes and UTF-8.
func archiveBlank(raw []byte) bool {
	for i := 1; i < len(raw)-1; {
		var r rune
		if raw[i] != '\\' {
			var n int
			r, n = utf8.DecodeRune(raw[i : len(raw)-1])
			i += n
		} else {
			i++
			switch raw[i] {
			case 't':
				r = '\t'
				i++
			case 'n':
				r = '\n'
				i++
			case 'r':
				r = '\r'
				i++
			case 'b':
				r = '\b'
				i++
			case 'f':
				r = '\f'
				i++
			case 'u':
				r = archiveHex(raw[i+1 : i+5])
				i += 5
				if utf16.IsSurrogate(r) {
					return false
				}
			default:
				return false
			}
		}
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
func archiveHex(raw []byte) rune {
	var r rune
	for _, c := range raw {
		r <<= 4
		switch {
		case c >= '0' && c <= '9':
			r += rune(c - '0')
		case c >= 'a' && c <= 'f':
			r += rune(c - 'a' + 10)
		default:
			r += rune(c - 'A' + 10)
		}
	}
	return r
}

func archiveField(v backend.JSONValue, name string) (backend.JSONValue, error) {
	var out backend.JSONValue
	if v.Kind() != '{' {
		return out, nil
	}
	err := v.EachField(func(key, value backend.JSONValue) error {
		if key.StringEquals(name) {
			out = value
		}
		return nil
	})
	return out, err
}

func (a *Archive) Member(b *backend.BuildBounds, ordinal int) (ArchiveMember, error) {
	if err := b.CheckContext(); err != nil {
		return ArchiveMember{}, err
	}
	if a == nil || ordinal < 0 || ordinal >= len(a.slots) {
		return ArchiveMember{}, errors.New("archive member ordinal out of range")
	}
	s := a.slots[ordinal]
	member := ArchiveMember{Missing: !s.found}
	if a.textMode {
		return a.textMember(b, ordinal, s)
	}
	if !s.found && !a.revise {
		return member, nil
	}
	if s.found {
		if s.target.Kind() != '"' {
			member.Failure = "selected translation has no translatable string"
			return member, nil
		}
		if n := s.target.DecodedStringBytes(); n > limitText(b) {
			return member, &responsecontract.MemberContentError{Ordinal: ordinal, Kind: responsecontract.MemberTooLarge, Limit: limitText(b), Observed: n}
		}
		target, err := s.target.StringLimitedBounded(b, limitText(b))
		if err != nil {
			return member, err
		}
		if a.revise {
			target = strings.TrimSpace(target)
			if err := backend.ReserveSlice[prompt.ReviseRevision](b, 1); err != nil {
				return member, err
			}
			member.Revision = &prompt.ReviseRevision{ID: a.ids[ordinal], Target: target}
		} else {
			member.Target = target
		}
	}
	rows := s.publicRuby
	if rows.Kind() == 0 && !a.revise {
		rows = s.ruby
	}
	if rows.Kind() != 0 {
		entries, err := ruby.ParseOutputEntriesBounded(b, rows.Raw())
		if errors.Is(err, backend.ErrBuildCapacity) {
			return member, err
		}
		if err == nil {
			member.RubyOutput = entries
		}
	}
	return member, nil
}

func (a *Archive) Glossary(b *backend.BuildBounds) ([]prompt.BootstrapEntry, error) {
	if err := b.CheckContext(); err != nil {
		return nil, err
	}
	if a == nil {
		return nil, errors.New("nil archive")
	}
	var entries []prompt.BootstrapEntry
	if a.textMode {
		for _, line := range a.glossaryLines {
			source, rest, ok := strings.Cut(line, "|")
			if !ok {
				continue
			}
			target, notes, _ := strings.Cut(rest, "|")
			entry := prompt.BootstrapEntry{Source: strings.TrimSpace(source), Target: strings.TrimSpace(target), Notes: strings.TrimSpace(notes)}
			if entry.Source == "" || entry.Target == "" {
				continue
			}
			var owned prompt.BootstrapEntry
			if err := b.CloneInto(entry, &owned); err != nil {
				return nil, err
			}
			var err error
			entries, err = backend.AppendNode(b, entries, owned)
			if err != nil {
				return nil, err
			}
		}
		return entries, nil
	}
	if a.glossary.Kind() == 0 {
		return nil, nil
	}
	err := a.glossary.DecodeIntoBounded(b, &entries, b.OutputLimit())
	if errors.Is(err, backend.ErrBuildCapacity) {
		return nil, err
	}
	return entries, nil
}
