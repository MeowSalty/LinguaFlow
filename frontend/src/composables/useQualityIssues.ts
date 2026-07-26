import type { VNode } from 'vue'
import { h } from 'vue'

import type { ApiSchemas } from '@/api/client'
import { t } from '@/i18n'

export type QualityIssue = ApiSchemas['QualityIssue']
export type QualityIssueSpan = ApiSchemas['QualityIssueSpan']

export type QualityCode =
  | 'untranslated'
  | 'length_ratio'
  | 'duplicate'
  | 'source_residual'
  | 'calque'
  | 'term_fidelity'
  | 'naturalness'

export interface QualityHighlightRange {
  start: number
  end: number
  severity: 'warning' | 'error'
}

const QUALITY_CODE_I18N_KEYS: Record<string, string> = {
  untranslated: 'untranslated',
  length_ratio: 'lengthRatio',
  duplicate: 'duplicate',
  source_residual: 'sourceResidual',
  calque: 'calque',
  term_fidelity: 'termFidelity',
  naturalness: 'naturalness',
}

/** 将 snake_case 问题代码转为 i18n 键名 */
export const qualityCodeToI18nKey = (code: string): string =>
  QUALITY_CODE_I18N_KEYS[code] ?? code.replace(/_([a-z])/g, (_, c: string) => c.toUpperCase())

export const getQualityCodeLabel = (code: string): string => {
  const key = `workspace.segment.qualityCodes.${qualityCodeToI18nKey(code)}`
  const label = t(key)
  return label === key ? code : label
}

export const formatQualityIssueTooltip = (issue: QualityIssue): string => {
  const codeLabel = getQualityCodeLabel(issue.code)
  const lines = [`[${codeLabel}] ${issue.message}`]
  const matched = issue.span?.matched_text?.trim()
  if (matched) {
    lines.push(t('workspace.segment.qualityMatched', { text: matched }))
  }
  return lines.join('\n')
}

const severityRank = (severity: 'warning' | 'error'): number => (severity === 'error' ? 2 : 1)

/** 在目标文本中按 rune 偏移（与后端一致）收集高亮区间 */
export const collectQualityHighlightRanges = (
  text: string,
  issues?: QualityIssue[],
): QualityHighlightRange[] => {
  if (!text || !issues?.length) return []

  const runes = Array.from(text)
  const ranges: QualityHighlightRange[] = []

  for (const issue of issues) {
    const span = issue.span
    if (!span) continue

    let start = span.target_start
    let end = span.target_end

    if (
      start == null ||
      end == null ||
      !Number.isFinite(start) ||
      !Number.isFinite(end) ||
      end <= start
    ) {
      const matched = span.matched_text?.trim()
      if (!matched) continue
      const matchedRunes = Array.from(matched)
      const found = findRuneIndex(runes, matchedRunes)
      if (found < 0) continue
      start = found
      end = found + matchedRunes.length
    }

    const clampedStart = Math.max(0, Math.min(start, runes.length))
    const clampedEnd = Math.max(clampedStart, Math.min(end, runes.length))
    if (clampedEnd <= clampedStart) continue

    ranges.push({
      start: clampedStart,
      end: clampedEnd,
      severity: issue.severity,
    })
  }

  return mergeHighlightRanges(ranges)
}

const findRuneIndex = (haystack: string[], needle: string[]): number => {
  if (!needle.length || needle.length > haystack.length) return -1
  outer: for (let i = 0; i <= haystack.length - needle.length; i++) {
    for (let j = 0; j < needle.length; j++) {
      if (haystack[i + j] !== needle[j]) continue outer
    }
    return i
  }
  return -1
}

/** 合并重叠区间；error 优先于 warning */
export const mergeHighlightRanges = (ranges: QualityHighlightRange[]): QualityHighlightRange[] => {
  if (ranges.length <= 1) return ranges.map((r) => ({ ...r }))

  const sorted = [...ranges].sort((a, b) => a.start - b.start || a.end - b.end)
  const merged: QualityHighlightRange[] = []

  for (const range of sorted) {
    const last = merged[merged.length - 1]
    if (!last || range.start > last.end) {
      merged.push({ ...range })
      continue
    }
    if (severityRank(range.severity) > severityRank(last.severity)) {
      last.severity = range.severity
    }
    last.end = Math.max(last.end, range.end)
  }

  return merged
}

/** 将目标文本渲染为带质量问题高亮的 VNode（纯文本模式） */
export const renderQualityHighlightedText = (text: string, issues?: QualityIssue[]): VNode => {
  const ranges = collectQualityHighlightRanges(text, issues)
  if (!ranges.length) {
    return h('span', null, text)
  }

  const runes = Array.from(text)
  const children: (string | VNode)[] = []
  let cursor = 0

  for (const range of ranges) {
    if (range.start > cursor) {
      children.push(runes.slice(cursor, range.start).join(''))
    }
    children.push(
      h(
        'mark',
        {
          class:
            range.severity === 'error'
              ? 'quality-span quality-span--error'
              : 'quality-span quality-span--warning',
        },
        runes.slice(range.start, range.end).join(''),
      ),
    )
    cursor = range.end
  }

  if (cursor < runes.length) {
    children.push(runes.slice(cursor).join(''))
  }

  return h('span', { class: 'quality-highlighted-text' }, children)
}
