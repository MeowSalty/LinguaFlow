package script

// Evidence 是扫描到的与期望文字系统相悖的证据。
type Evidence struct {
	Script Script // 兄弟文字系统
	Count  int    // 该系统专属字符出现总数（按 rune 计，重复累计）
	Sample string // 首个连续证据 run 的原文（同 script 专属字符的连续片段；上限 12 rune，超出截断）
}

// maxSampleRunes 是 Evidence.Sample 的截断上限：够上层定位与指纹比对，又不易超长。
const maxSampleRunes = 12

// Scan 统计 text 中落在 Profile.Siblings 各文字系统的专属字符。
// 只统计 runeScript 判为某个 sibling 的 rune；共用/中性字符不计，且会中断连续 run。
// 存在证据时返回计数最大的兄弟系统（并列取 Siblings 顺序靠前者）；无证据返回 (zero, false)。
func (p Profile) Scan(text string) (Evidence, bool) {
	counts := make(map[Script]int, len(p.Siblings))
	first := make(map[Script]string, len(p.Siblings)) // 各 sibling 首个连续 run（截断至 maxSampleRunes）
	var cur Script                                    // 当前 run 的文字系统，零值表示不在任何 run 中
	var run []rune
	runIsFirst := false // 当前 run 是否为该文字系统的首个 run

	for _, r := range text {
		sc, ok := runeScript(r)
		if !ok || !isSibling(p.Siblings, sc) {
			// 期望侧专属、共用或中性字符：中断当前连续 run
			cur = ""
			run = nil
			runIsFirst = false
			continue
		}
		counts[sc]++
		if sc != cur {
			cur = sc
			run = make([]rune, 0, maxSampleRunes)
			if _, seen := first[sc]; !seen {
				runIsFirst = true // 仅记录首个 run
			}
		}
		if len(run) < maxSampleRunes {
			run = append(run, r)
			if runIsFirst {
				// 同步切片头到 first，后续 append 不会自动更新已存 header
				first[sc] = string(run)
			}
		}
	}

	// 并列取 Siblings 顺序靠前者：严格大于才替换
	best := Script("")
	bestCount := 0
	for _, s := range p.Siblings {
		if c := counts[s]; c > bestCount {
			best, bestCount = s, c
		}
	}
	if best == "" {
		return Evidence{}, false
	}
	return Evidence{Script: best, Count: bestCount, Sample: first[best]}, true
}

// isSibling 判断 s 是否在 siblings 集合内。
func isSibling(siblings []Script, s Script) bool {
	for _, x := range siblings {
		if x == s {
			return true
		}
	}
	return false
}
