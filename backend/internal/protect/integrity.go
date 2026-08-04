package protect

import (
	"regexp"
	"sort"
	"strings"

	"github.com/MeowSalty/LinguaFlow/backend/internal/model"
)

var placeholderLikeRE = regexp.MustCompile(`(?i)__LF_[A-Z0-9_]+?__`)

// PlaceholderViolations 对段做占位符全守恒校验（还原前）：
//
//   - missing：Protected 中计数为 0 的 key
//   - duplicated：Protected 中计数 >1 的 key
//   - invented：Target 中出现的规范形态 __LF_######__ 且不在 Protected 中的 token
//
// 已知 key 即使重复也只记 duplicated，不记 invented。
// 三类结果均按 key 升序排序，便于日志稳定输出。
// 调用方应在可选的 NormalizePlaceholders 之后调用本函数。
func PlaceholderViolations(seg *model.Segment) (missing, duplicated, invented []string) {
	if seg == nil {
		return nil, nil, nil
	}
	target := seg.Target

	if len(seg.Protected) > 0 {
		for k := range seg.Protected {
			n := strings.Count(target, k)
			switch {
			case n == 0:
				missing = append(missing, k)
			case n > 1:
				duplicated = append(duplicated, k)
			}
		}
		sort.Strings(missing)
		sort.Strings(duplicated)
	}

	seenInvented := map[string]struct{}{}
	for _, tok := range placeholderLikeRE.FindAllString(target, -1) {
		if _, known := seg.Protected[tok]; known {
			continue
		}
		if _, seen := seenInvented[tok]; seen {
			continue
		}
		seenInvented[tok] = struct{}{}
		invented = append(invented, tok)
	}
	sort.Strings(invented)
	return missing, duplicated, invented
}
