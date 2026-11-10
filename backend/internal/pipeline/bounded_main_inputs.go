package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"unicode/utf8"

	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/glossary"
	"github.com/MeowSalty/LinguaFlow/backend/internal/prompt"
	"github.com/MeowSalty/LinguaFlow/backend/internal/tm"
)

// ExpandMainContextBounded preserves the configured symmetric context window
// and rune truncation. Count and exact backing tickets precede every array or
// truncated string allocation; ordinary context text remains borrowed.
func ExpandMainContextBounded(b *backend.BuildBounds, doc *Document, idxs []int, totalSegments, window, maxChars int, maxBytes int64) (expandedContext, error) {
	if totalSegments < 0 || totalSegments > len(doc.Segments) || maxBytes < 0 {
		return expandedContext{}, backend.ErrBuildCapacity
	}
	for _, idx := range idxs {
		if idx < 0 || idx >= totalSegments {
			return expandedContext{}, backend.ErrBuildCapacity
		}
	}
	if e := b.ReserveNodes(int64(len(idxs))+3, 1024); e != nil {
		return expandedContext{}, e
	}
	batch := make(map[int]struct{}, len(idxs))
	for _, idx := range idxs {
		batch[idx] = struct{}{}
	}
	lo, hi := 0, -1
	if len(idxs) > 0 {
		lo, hi = idxs[0], idxs[len(idxs)-1]
		if window > 0 {
			if window >= lo {
				lo = 0
			} else {
				lo -= window
			}
			if window >= totalSegments-1-hi {
				hi = totalSegments - 1
			} else {
				hi += window
			}
		}
	}
	eligible := func(i int) bool {
		if _, ok := batch[i]; ok {
			return true
		}
		return window > 0 && isContextEligible(&doc.Segments[i])
	}
	count, truncatedCount := 0, 0
	truncatedBytes := int64(0)
	for i := lo; i <= hi; i++ {
		if !eligible(i) {
			continue
		}
		count++
		if _, own := batch[i]; own || window <= 0 || maxChars <= 0 {
			continue
		}
		source := rawSource(&doc.Segments[i])
		prefix, cut := measureRunePrefix(source, maxChars)
		if !cut {
			continue
		}
		n := prefix + int64(len("…"))
		if n > maxBytes-truncatedBytes {
			return expandedContext{}, backend.ErrBuildCapacity
		}
		truncatedBytes += n
		truncatedCount++
	}
	// With no window the original input order, including duplicates, is retained.
	if window <= 0 {
		count = len(idxs)
	}
	if e := b.ReserveNodes(int64(count), 8); e != nil {
		return expandedContext{}, e
	}
	if e := b.ReserveNodes(1, 24); e != nil {
		return expandedContext{}, e
	}
	if e := b.ReserveNodes(int64(truncatedCount)+1, 1024); e != nil {
		return expandedContext{}, e
	}
	result := expandedContext{Idxs: make([]int, 0, count), TruncatedSrc: make(map[int]string, truncatedCount)}
	if window <= 0 {
		result.Idxs = append(result.Idxs, idxs...)
		return result, nil
	}
	for i := lo; i <= hi; i++ {
		if !eligible(i) {
			continue
		}
		result.Idxs = append(result.Idxs, i)
		if _, own := batch[i]; own || maxChars <= 0 {
			continue
		}
		source := rawSource(&doc.Segments[i])
		prefix, cut := measureRunePrefix(source, maxChars)
		if !cut {
			continue
		}
		w, e := backend.NewFixedBoundedWriter(b, prefix+int64(len("…")))
		if e != nil {
			return expandedContext{}, e
		}
		var scratch [utf8.UTFMax]byte
		runes := 0
		for _, r := range source {
			if runes == maxChars {
				break
			}
			part := utf8.AppendRune(scratch[:0], r)
			if _, e = w.Write(part); e != nil {
				return expandedContext{}, e
			}
			runes++
		}
		if _, e = w.WriteString("…"); e != nil {
			return expandedContext{}, e
		}
		if e = b.ReserveText(int64(len(w.Bytes())), int64(len(w.Bytes()))); e != nil {
			return expandedContext{}, e
		}
		result.TruncatedSrc[i] = string(w.Bytes())
	}
	return result, nil
}

func measureRunePrefix(source string, maxChars int) (int64, bool) {
	n, bytes := 0, int64(0)
	for _, r := range source {
		if n == maxChars {
			return bytes, true
		}
		bytes += int64(utf8.RuneLen(r))
		n++
	}
	return bytes, false
}

// PlanFirstMainBatchBounded consumes only the fair station's eligible indices.
// It never creates a document-sized prefix, analyses or expanded context tree.
func PlanFirstMainBatchBounded(b *backend.BuildBounds, h stagedHandler, doc *Document, eligible []int, pool int) ([]int, error) {
	if len(eligible) == 0 {
		return nil, nil
	}
	if e := b.ReserveNodes(4, 1024); e != nil {
		return nil, e
	}
	for i, idx := range eligible {
		if idx < 0 || idx >= len(doc.Segments) || (i > 0 && idx <= eligible[i-1]) {
			return nil, backend.ErrBuildCapacity
		}
	}
	constraint := BatchConstraint{}
	span, window := 0, 0
	translate := false
	switch handler := h.(type) {
	case *TranslateHandler:
		translate = true
		constraint.MaxSegments = handler.BatchSize
		constraint.MaxWords = handler.MaxWordsPerBatch
		if constraint.MaxSegments <= 0 && constraint.MaxWords <= 0 {
			constraint.MaxSegments = 1
		}
		constraint = shrinkConstraint(constraint, handler.FallbackShrink, pool)
		if handler.Context.Enabled {
			window = max(handler.Context.Before, handler.Context.After)
		}
	case *ReviseHandler:
		constraint.MaxSegments = handler.BatchSize
		constraint.MaxWords = handler.MaxWordsPerBatch
		span = handler.MaxBatchIndexSpan
		constraint.WordCount = func(seg Segment) int {
			a, z := CountWords(seg.Source), CountWords(seg.Target)
			if a > math.MaxInt-z {
				return math.MaxInt
			}
			return a + z
		}
	default:
		return nil, errors.New("unsupported bounded main planner")
	}
	cut := func(lo, hi int, withContext bool) int {
		words := 0
		for i := lo; i < hi; i++ {
			next := CountWords(doc.Segments[eligible[i]].Source)
			if constraint.WordCount != nil {
				next = constraint.WordCount(doc.Segments[eligible[i]])
			}
			if i > lo {
				exceeds := constraint.exceeds(i-lo, words, next) || (span > 0 && eligible[i]-eligible[lo] > span)
				if withContext && constraint.MaxWords > 0 {
					contextWords := mainContextWordsScalar(doc, eligible[lo:i+1], window)
					exceeds = exceeds || contextWords > constraint.MaxWords || words > constraint.MaxWords-contextWords || next > constraint.MaxWords-contextWords-words
				}
				if exceeds {
					return i
				}
			}
			if next > math.MaxInt-words {
				words = math.MaxInt
			} else {
				words += next
			}
		}
		return hi
	}
	lo, hi := 0, len(eligible)
	if !translate {
		hi = cut(0, hi, false)
	} else if window > 0 {
		end := len(eligible)
		for i := 1; i < len(eligible); i++ {
			gap := eligible[i] - eligible[i-1]
			if window < (math.MaxInt-1)/2 && gap > 2*window+1 {
				end = i
				break
			}
		}
		hi = cut(0, end, true)
	} else {
		// Continuous batches publish all split prefixes before sorted leftovers.
		bestLo, bestHi := 0, 0
		for start, end := 0, 1; end <= len(eligible); end++ {
			if end < len(eligible) && eligible[end] == eligible[end-1]+1 {
				continue
			}
			firstEnd := cut(start, end, false)
			if firstEnd < end {
				bestLo, bestHi = start, firstEnd
				break
			}
			if end-start > bestHi-bestLo || (end-start == bestHi-bestLo && eligible[start] < eligible[bestLo]) {
				bestLo, bestHi = start, end
			}
			start = end
		}
		lo, hi = bestLo, bestHi
	}
	if e := b.ReserveNodes(int64(hi-lo), 8); e != nil {
		return nil, e
	}
	if e := b.ReserveNodes(1, 24); e != nil {
		return nil, e
	}
	result := make([]int, hi-lo)
	copy(result, eligible[lo:hi])
	return result, nil
}

func mainContextWordsScalar(doc *Document, candidate []int, window int) int {
	lo, hi := candidate[0], candidate[len(candidate)-1]
	if window >= lo {
		lo = 0
	} else {
		lo -= window
	}
	if window >= len(doc.Segments)-1-hi {
		hi = len(doc.Segments) - 1
	} else {
		hi += window
	}
	words, at := 0, 0
	for i := lo; i <= hi; i++ {
		for at < len(candidate) && candidate[at] < i {
			at++
		}
		if at < len(candidate) && candidate[at] == i || !isContextEligible(&doc.Segments[i]) {
			continue
		}
		n := CountWords(rawSource(&doc.Segments[i]))
		if n > math.MaxInt-words {
			return math.MaxInt
		}
		words += n
	}
	return words
}

// MainHintPayload is the only hint value permitted across an admission wait.
// Its actual byte length belongs to the prepared input K, not dictionary H.
type MainHintPayload struct {
	JSON []byte
	// AttachmentBytes is the peak input reservation during collection; the
	// caller reprices it to len(JSON) after transient keys/metadata are dropped.
	AttachmentBytes int64
}
type mainHints struct {
	Glossary []prompt.GlossaryEntry `json:"glossary"`
	TM       []prompt.TMHint        `json:"tm"`
}
type hintPair struct{ Source, Target string }
type mainHintRecords struct {
	Glossary []json.RawMessage `json:"glossary"`
	TM       []json.RawMessage `json:"tm"`
}

func DecodeMainHintsBounded(b *backend.BuildBounds, p MainHintPayload) ([]prompt.GlossaryEntry, []prompt.TMHint, error) {
	if len(p.JSON) == 0 {
		return nil, nil, nil
	}
	var hints mainHints
	if e := b.DecodeJSONLimited(p.JSON, &hints, int64(len(p.JSON))); e != nil {
		return nil, nil, e
	}
	return hints.Glossary, hints.TM, nil
}

// LookupMainHintsBounded visits immutable glossary entries and finite TM rows.
// Typed arrays are sealed under the short runner and cannot accompany a later
// grant wait. Duplicate pair keys borrow two strings; no combined key is built.
func LookupMainHintsBounded(ctx context.Context, h *TranslateHandler, doc *Document, idxs []int, maxBytes int64, run func(context.Context, func() error) error, logger *slog.Logger) (MainHintPayload, error) {
	return LookupMainHintsWithCreditsBounded(ctx, h, doc, idxs, maxBytes, run, logger, nil)
}

// claim grants the measured encoded attachment before its backing allocation.
// It is nonblocking; capacity waits belong outside the short runner.
func LookupMainHintsWithCreditsBounded(ctx context.Context, h *TranslateHandler, doc *Document, idxs []int, maxBytes int64, run func(context.Context, func() error) error, logger *slog.Logger, claim func(int64) error) (MainHintPayload, error) {
	if ctx.Err() != nil {
		return MainHintPayload{}, ctx.Err()
	}
	b, e := backend.NewBuildBounds(maxBytes, maxBytes)
	if e != nil {
		return MainHintPayload{}, e
	}
	if run == nil {
		run = func(ctx context.Context, fn func() error) error { return backend.RunBuild(ctx, fn) }
	}
	var hints mainHintRecords
	var notesBytes []int64
	var scores []float32
	var gi, ti map[hintPair]int
	bytes := int64(0)
	fragmentBytes, lastClaimed := int64(0), int64(0)
	var claimErr error
	e = run(ctx, func() error {
		if e := b.ReserveNodes(6, 1024); e != nil {
			return e
		}
		gi = make(map[hintPair]int)
		ti = make(map[hintPair]int)
		hints.Glossary = make([]json.RawMessage, 0)
		hints.TM = make([]json.RawMessage, 0)
		return nil
	})
	if e != nil {
		return MainHintPayload{}, e
	}
	reserveRecord := func(value any, oldBytes int64, gCount, tCount int) (int64, error) {
		n, e := b.MeasureJSON(value)
		if e != nil {
			return 0, e
		}
		future := fragmentBytes - oldBytes + n
		if future > maxBytes {
			return 0, backend.ErrBuildCapacity
		}
		encoded := future + int64(len("{\"glossary\":[],\"tm\":[]}")) + int64(max(0, gCount-1)) + int64(max(0, tCount-1))
		// Concrete array/map records fit one E per row, plus one E fixed state.
		metadata := int64(gCount+tCount+1) * 1024
		if encoded > maxBytes-metadata {
			return 0, backend.ErrBuildCapacity
		}
		attachment := encoded + metadata
		if attachment > lastClaimed {
			if claim != nil {
				if e = claim(attachment); e != nil {
					claimErr = e
					return 0, e
				}
			}
			lastClaimed = attachment
		}
		return n, nil
	}
	var currentEntry glossary.Entry
	addGlossaryBuild := func() error {
		entry := currentEntry
		if entry.Forbidden {
			return nil
		}
		key := hintPair{entry.Source, entry.Target}
		old, exists := gi[key]
		n := int64(len(entry.Source)) + int64(len(entry.Target)) + int64(len(entry.Notes))
		if exists {
			n = int64(len(entry.Notes)) - notesBytes[old]
		}
		if n > maxBytes-bytes {
			return backend.ErrBuildCapacity
		}
		bytes += n
		value := prompt.GlossaryEntry{Source: entry.Source, Target: entry.Target, Notes: entry.Notes}
		// Passing this value to the reflective codec boxes its concrete record.
		if e := b.ReserveNodes(1, 1024); e != nil {
			return e
		}
		oldEncoded := int64(0)
		gCount := len(hints.Glossary)
		if exists {
			oldEncoded = int64(len(hints.Glossary[old]))
		} else {
			gCount++
		}
		encoded, e := reserveRecord(value, oldEncoded, gCount, len(hints.TM))
		if e != nil {
			return e
		}
		raw, e := b.EncodeJSON(value, maxBytes)
		if e != nil {
			return e
		}
		fragmentBytes = fragmentBytes - oldEncoded + encoded
		if exists {
			hints.Glossary[old] = raw
			notesBytes[old] = int64(len(entry.Notes))
			return nil
		}
		if e := b.ReserveNodes(1, 1024); e != nil {
			return e
		}
		gi[key] = len(hints.Glossary)
		hints.Glossary, e = backend.AppendNode(b, hints.Glossary, json.RawMessage(raw))
		if e != nil {
			return e
		}
		notesBytes, e = backend.AppendNode(b, notesBytes, int64(len(entry.Notes)))
		return e
	}
	addGlossary := func(entry glossary.Entry) error {
		currentEntry = entry
		e := run(ctx, addGlossaryBuild)
		currentEntry = glossary.Entry{}
		return e
	}
	var currentMatch tm.Match
	addTMBuild := func() error {
		match := currentMatch
		key := hintPair{match.Source, match.Target}
		old, exists := ti[key]
		if exists {
			if match.Score <= scores[old] {
				return nil
			}
		}
		n := int64(0)
		if !exists {
			n = int64(len(match.Source)) + int64(len(match.Target))
		}
		if n > maxBytes-bytes {
			return backend.ErrBuildCapacity
		}
		bytes += n
		value := prompt.TMHint{Source: match.Source, Target: match.Target, Score: match.Score}
		if e := b.ReserveNodes(1, 1024); e != nil {
			return e
		}
		oldEncoded := int64(0)
		tCount := len(hints.TM)
		if exists {
			oldEncoded = int64(len(hints.TM[old]))
		} else {
			tCount++
		}
		encoded, e := reserveRecord(value, oldEncoded, len(hints.Glossary), tCount)
		if e != nil {
			return e
		}
		raw, e := b.EncodeJSON(value, maxBytes)
		if e != nil {
			return e
		}
		fragmentBytes = fragmentBytes - oldEncoded + encoded
		if exists {
			hints.TM[old] = raw
			scores[old] = match.Score
			return nil
		}
		if e := b.ReserveNodes(1, 1024); e != nil {
			return e
		}
		ti[key] = len(hints.TM)
		hints.TM, e = backend.AppendNode(b, hints.TM, json.RawMessage(raw))
		if e != nil {
			return e
		}
		scores, e = backend.AppendNode(b, scores, match.Score)
		return e
	}
	addTM := func(match tm.Match) error {
		currentMatch = match
		e := run(ctx, addTMBuild)
		currentMatch = tm.Match{}
		return e
	}
	for _, idx := range idxs {
		if idx < 0 || idx >= len(doc.Segments) {
			return MainHintPayload{}, backend.ErrBuildCapacity
		}
		seg := &doc.Segments[idx]
		if h.Glossary != nil {
			visitor, ok := h.Glossary.(glossary.EntryVisitor)
			if !ok {
				return MainHintPayload{}, errors.New("glossary lacks bounded entry visitor")
			}
			e = visitor.VisitMatchesBounded(ctx, seg.Source, doc.SourceLang, doc.TargetLang, b, run, addGlossary)
			if e != nil {
				if claimErr != nil {
					return MainHintPayload{}, claimErr
				}
				if errors.Is(e, backend.ErrBuildCapacity) || storageFailure(e) || ctx.Err() != nil {
					return MainHintPayload{}, e
				}
				if logger != nil {
					logger.Warn("glossary lookup failed", "err", e, "seg", seg.ID)
				}
			}
		}
		if h.TM != nil {
			readContext := tm.WithInputReadCredit(ctx, func(n int64) error {
				// The next SQL tuple is granted before its body read. Existing
				// opaque fragments and lossless scalar keys each fit their K term.
				if n < 0 || n > maxBytes {
					return backend.ErrBuildCapacity
				}
				metadata := int64(len(hints.Glossary)+len(hints.TM)+2) * 1024
				if fragmentBytes > math.MaxInt64-bytes || fragmentBytes+bytes > math.MaxInt64-n-metadata {
					return backend.ErrBuildCapacity
				}
				owned := fragmentBytes + bytes + n + metadata
				desired := owned/2 + owned%2
				if desired > maxBytes {
					return backend.ErrBuildCapacity
				}
				if desired > lastClaimed {
					if claim != nil {
						if err := claim(desired); err != nil {
							claimErr = err
							return err
						}
					}
					lastClaimed = desired
				}
				return nil
			})
			e = tm.VisitMatchesBounded(readContext, h.TM, seg.Source, doc.SourceLang, doc.TargetLang, b, run, addTM)
			if e != nil {
				if claimErr != nil {
					return MainHintPayload{}, claimErr
				}
				if errors.Is(e, backend.ErrBuildCapacity) || storageFailure(e) || ctx.Err() != nil {
					return MainHintPayload{}, e
				}
				if logger != nil {
					logger.Warn("tm search failed", "err", e, "seg", seg.ID)
				}
			}
		}
	}
	var payload MainHintPayload
	e = run(ctx, func() error {
		if len(hints.Glossary) == 0 && len(hints.TM) == 0 {
			return nil
		}
		// Lossless scalar keys and metadata are dead before the combined output
		// backing is allocated; only opaque fragments accompany that allocation.
		gi, ti = nil, nil
		notesBytes = nil
		scores = nil
		n, measureErr := b.MeasureJSON(hints)
		if measureErr != nil {
			return measureErr
		}
		if n > maxBytes {
			return backend.ErrBuildCapacity
		}
		if n > lastClaimed {
			return backend.ErrBuildCapacity
		}
		var e error
		payload.JSON, e = b.EncodeJSON(hints, maxBytes)
		payload.AttachmentBytes = lastClaimed
		hints = mainHintRecords{}
		gi, ti = nil, nil
		return e
	})
	return payload, e
}
