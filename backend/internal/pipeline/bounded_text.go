package pipeline

import (
	"github.com/MeowSalty/LinguaFlow/backend/internal/backend"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ruby"
	"sort"
	"strings"
)

func replaceAllBounded(b *backend.BuildBounds, text, from, to string, limit int64) (string, error) {
	if from == "" || from == to {
		return text, nil
	}
	count := strings.Count(text, from)
	if count == 0 {
		return text, nil
	}
	size, err := boundedSum(int64(len(text)-count*len(from)), int64(count)*int64(len(to)))
	if err != nil || size > limit {
		return "", backend.ErrBuildCapacity
	}
	w, err := backend.NewFixedBoundedWriter(b, size)
	if err != nil {
		return "", err
	}
	for {
		i := strings.Index(text, from)
		if i < 0 {
			_, err = w.WriteString(text)
			break
		}
		if _, err = w.WriteString(text[:i]); err != nil {
			break
		}
		if _, err = w.WriteString(to); err != nil {
			break
		}
		text = text[i+len(from):]
	}
	if err != nil {
		return "", err
	}
	if err = b.ReserveText(size, size); err != nil {
		return "", err
	}
	return string(w.Bytes()), nil
}
func mapSnippetToProtectedFormBounded(b *backend.BuildBounds, text string, mapping map[string]string) (string, error) {
	var err error
	if strings.Contains(text, "<ruby>") {
		text, err = ruby.StripRubyTextBounded(b, text)
		if err != nil {
			return "", err
		}
	}
	if err = b.ReserveNodes(int64(len(mapping)), 16); err != nil {
		return "", err
	}
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := mapping[keys[i]], mapping[keys[j]]
		if len(left) != len(right) {
			return len(left) > len(right)
		}
		return keys[i] > keys[j]
	})
	for _, key := range keys {
		if value := mapping[key]; value != "" {
			text, err = replaceAllBounded(b, text, value, key, b.OutputLimit())
			if err != nil {
				return "", err
			}
		}
	}
	return text, nil
}
