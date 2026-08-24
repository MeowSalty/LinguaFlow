import type { VNode } from 'vue'
import { h } from 'vue'

import type { ApiSchemas } from '@/api/client'
import type { ResourceSegmentQualityCode } from '@/api/projects'
import { t } from '@/i18n'

export type QualityIssue = ApiSchemas['QualityIssue']
export type QualityIssueSpan = ApiSchemas['QualityIssueSpan']

export type QualityCode = ResourceSegmentQualityCode

export interface QualityHighlightRange {
  start: number
  end: number
  severity: 'warning' | 'error'
  /** 已被驳回（dismissed）的 issue：渲染为淡灰色而非 warning/error */
  dismissed?: boolean
}

const QUALITY_CODE_I18N_KEYS: Record<QualityCode, string> = {
  untranslated: 'untranslated',
  length_ratio: 'lengthRatio',
  duplicate: 'duplicate',
  source_residual: 'sourceResidual',
  punctuation_pairing: 'punctuationPairing',
  punctuation_missing: 'punctuationMissing',
  punctuation_surplus: 'punctuationSurplus',
  punctuation_wrap_loss: 'punctuationWrapLoss',
  whitespace_irregular: 'whitespaceIrregular',
  repeated_space: 'repeatedSpace',
  width_mix: 'widthMix',
  number_mismatch: 'numberMismatch',
  url_email_mismatch: 'urlEmailMismatch',
  subtitle_line_count: 'subtitleLineCount',
  forbidden_term: 'forbiddenTerm',
  term_inconsistency: 'termInconsistency',
  leftover_placeholder: 'leftoverPlaceholder',
  xml_tag_mismatch: 'xmlTagMismatch',
  duplicate_source_divergence: 'duplicateSourceDivergence',
  calque: 'calque',
  term_fidelity: 'termFidelity',
  naturalness: 'naturalness',
  mistranslation: 'mistranslation',
  omission: 'omission',
  addition: 'addition',
  grammar: 'grammar',
  register: 'register',
}

/** 段落筛选 chip 的语义分组；顺序即展示顺序 */
export const QUALITY_CODE_GROUPS: { key: string; codes: QualityCode[] }[] = [
  { key: 'hardRule', codes: ['untranslated', 'duplicate', 'source_residual', 'length_ratio'] },
  {
    key: 'typography',
    codes: [
      'punctuation_pairing',
      'punctuation_missing',
      'punctuation_surplus',
      'punctuation_wrap_loss',
      'whitespace_irregular',
      'repeated_space',
      'width_mix',
    ],
  },
  { key: 'content', codes: ['number_mismatch', 'url_email_mismatch'] },
  { key: 'markup', codes: ['leftover_placeholder', 'xml_tag_mismatch'] },
  { key: 'term', codes: ['forbidden_term', 'term_inconsistency', 'term_fidelity'] },
  {
    key: 'semantic',
    codes: [
      'calque',
      'naturalness',
      'mistranslation',
      'omission',
      'addition',
      'grammar',
      'register',
    ],
  },
  { key: 'subtitle', codes: ['subtitle_line_count'] },
  { key: 'divergence', codes: ['duplicate_source_divergence'] },
]

export const QUALITY_CODES = Object.keys(QUALITY_CODE_I18N_KEYS) as QualityCode[]

/** revise 轮 / 修订预览可作修复目标的语义 issue code 白名单（与 API 规范一致） */
export const SEMANTIC_REPAIR_ISSUE_CODES: QualityCode[] = [
  'calque',
  'term_fidelity',
  'naturalness',
  'mistranslation',
  'omission',
  'addition',
  'grammar',
  'register',
]

/** 该 issue code 是否属于修订可修复的语义白名单 */
export const isSemanticRepairIssueCode = (code: string): boolean =>
  (SEMANTIC_REPAIR_ISSUE_CODES as string[]).includes(code)

/** 将 snake_case 问题代码转为 i18n 键名 */
export const qualityCodeToI18nKey = (code: string): string =>
  QUALITY_CODE_I18N_KEYS[code as QualityCode] ??
  code.replace(/_([a-z])/g, (_, c: string) => c.toUpperCase())

export const getQualityCodeLabel = (code: string): string => {
  const key = `workspace.segment.qualityCodes.${qualityCodeToI18nKey(code)}`
  const label = t(key)
  return label === key ? code : label
}

/** 该问题是否已被驳回（dismissed）；省略 disposition 视为未决（pending） */
export const isIssueDismissed = (issue: QualityIssue): boolean => issue.disposition === 'dismissed'

/** 段落上是否存在 pending 的语义 issue（revise 轮 / 修订预览的修复目标） */
export const hasPendingSemanticIssues = (segment: { quality_issues?: QualityIssue[] }): boolean =>
  (segment.quality_issues ?? []).some(
    (issue) => !isIssueDismissed(issue) && isSemanticRepairIssueCode(issue.code),
  )

/** 格式化裁决信息为可读文本（用于 tooltip 末尾追加） */
export const formatQualityIssueDisposition = (issue: QualityIssue): string => {
  if (!isIssueDismissed(issue)) return ''
  const lines: string[] = [t('workspace.segment.disposition.dismissedHint')]
  if (issue.note) {
    lines.push(issue.note)
  }
  if (issue.decided_at) {
    const time = new Date(issue.decided_at).toLocaleString()
    lines.push(t('workspace.segment.disposition.decidedAt', { time }))
  }
  if (issue.decided_by != null) {
    lines.push(t('workspace.segment.disposition.decidedByUser', { userId: issue.decided_by }))
  } else if (issue.decided_at) {
    lines.push(t('workspace.segment.disposition.decidedByLlm'))
  }
  return lines.join('\n')
}

export const formatQualityIssueTooltip = (issue: QualityIssue): string => {
  const codeLabel = getQualityCodeLabel(issue.code)
  const lines = [`[${codeLabel}] ${issue.message}`]
  const matched = issue.span?.matched_text?.trim()
  if (matched) {
    lines.push(t('workspace.segment.qualityMatched', { text: matched }))
  }
  const disposition = formatQualityIssueDisposition(issue)
  if (disposition) {
    lines.push(disposition)
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
      dismissed: isIssueDismissed(issue),
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
    // 被驳回的 issue 覆盖正常高亮：任一被合并区间为 dismissed 即整体标记为 dismissed
    if (range.dismissed) {
      last.dismissed = true
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
          class: range.dismissed
            ? 'quality-span quality-span--dismissed'
            : range.severity === 'error'
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

// ── HTML 模式高亮 ──

/** 危险标签黑名单：解析时整体剔除对应标签子树（含内容），防止 XSS */
const HTML_BLOCKED_TAGS = new Set([
  'script',
  'style',
  'iframe',
  'object',
  'embed',
  'form',
  'input',
  'textarea',
  'button',
  'select',
  'link',
  'meta',
  'base',
])

/** 块级标签：构建可见文本时在边界插入 \n 占位，避免 matched_text 跨块误匹配 */
const HTML_BLOCK_TAGS = new Set([
  'address',
  'article',
  'aside',
  'blockquote',
  'dd',
  'div',
  'dl',
  'dt',
  'figcaption',
  'figure',
  'footer',
  'h1',
  'h2',
  'h3',
  'h4',
  'h5',
  'h6',
  'header',
  'hr',
  'li',
  'main',
  'nav',
  'ol',
  'p',
  'pre',
  'section',
  'table',
  'tbody',
  'td',
  'tfoot',
  'th',
  'thead',
  'tr',
  'ul',
])

export interface HtmlHighlightAtom extends QualityHighlightRange {
  /** 覆盖该原子段的 issue 索引列表 */
  issueIds: number[]
}

export interface HtmlHighlightLayout {
  /** 边界切分并合并后的原子段（可见文本 rune 坐标） */
  merged: HtmlHighlightAtom[]
  /** 每个 issue 的精确区间（可见文本 rune 坐标）；定位失败为 null */
  perIssue: (QualityHighlightRange | null)[]
}

interface HtmlTextMap {
  /** 可见文本 rune 数组（块级边界含 \n 占位） */
  runes: string[]
  /** Text 节点 → 其文本在可见文本中的 rune 起始偏移 */
  nodeStarts: Map<Text, number>
}

const parseHtmlBody = (html: string): HTMLElement =>
  new DOMParser().parseFromString(html, 'text/html').body

/** 遍历 body 构建可见文本（rune 数组）与文本节点映射；剔除危险标签，块级边界插入 \n */
const buildVisibleTextMap = (root: HTMLElement): HtmlTextMap => {
  let text = ''
  let runeCount = 0
  const nodeStarts = new Map<Text, number>()

  const ensureNewline = (): void => {
    if (runeCount > 0 && !text.endsWith('\n')) {
      text += '\n'
      runeCount += 1
    }
  }

  const walk = (node: Node): void => {
    if (node.nodeType === Node.TEXT_NODE) {
      const textNode = node as Text
      if (textNode.data) {
        nodeStarts.set(textNode, runeCount)
        text += textNode.data
        runeCount += Array.from(textNode.data).length
      }
      return
    }
    if (node.nodeType !== Node.ELEMENT_NODE) return
    const el = node as Element
    const tag = el.tagName.toLowerCase()
    if (HTML_BLOCKED_TAGS.has(tag)) return
    if (tag === 'br') {
      ensureNewline()
      return
    }
    const isBlock = HTML_BLOCK_TAGS.has(tag)
    if (isBlock) ensureNewline()
    for (const child of Array.from(el.childNodes)) walk(child)
    if (isBlock) ensureNewline()
  }

  for (const child of Array.from(root.childNodes)) walk(child)
  return { runes: Array.from(text), nodeStarts }
}

/** 自验证定位：优先用后端偏移在可见文本上切片校验 matched_text，失败回退可见文本搜索 */
const locateSpanInVisibleRunes = (
  runes: string[],
  span: QualityIssueSpan,
): { start: number; end: number } | null => {
  const matched = span.matched_text?.trim()
  const start = span.target_start
  const end = span.target_end

  if (
    start != null &&
    end != null &&
    Number.isFinite(start) &&
    Number.isFinite(end) &&
    end > start &&
    start >= 0 &&
    end <= runes.length
  ) {
    if (!matched || runes.slice(start, end).join('').trim() === matched) {
      return { start, end }
    }
  }

  if (!matched) return null
  const matchedRunes = Array.from(matched)
  const found = findRuneIndex(runes, matchedRunes)
  if (found < 0) return null
  return { start: found, end: found + matchedRunes.length }
}

/** 由 per-issue 区间构建边界切分的原子段（相邻同 severity 且同覆盖集合并） */
const buildHtmlHighlightLayout = (
  runes: string[],
  issues?: QualityIssue[],
): HtmlHighlightLayout => {
  if (!issues?.length) return { merged: [], perIssue: [] }

  const perIssue: (QualityHighlightRange | null)[] = []
  const endpoints = new Set<number>()

  for (const issue of issues) {
    const span = issue.span
    const located = span && runes.length ? locateSpanInVisibleRunes(runes, span) : null
    if (!located) {
      perIssue.push(null)
      continue
    }
    perIssue.push({
      start: located.start,
      end: located.end,
      severity: issue.severity,
      dismissed: isIssueDismissed(issue),
    })
    endpoints.add(located.start)
    endpoints.add(located.end)
  }

  const sorted = [...endpoints].sort((a, b) => a - b)
  const merged: HtmlHighlightAtom[] = []

  for (let i = 0; i < sorted.length - 1; i++) {
    const start = sorted[i]!
    const end = sorted[i + 1]!
    if (end <= start) continue

    const issueIds: number[] = []
    let severity: 'warning' | 'error' = 'warning'
    let dismissed = false
    perIssue.forEach((range, idx) => {
      if (range && range.start <= start && range.end >= end) {
        issueIds.push(idx)
        if (range.severity === 'error') severity = 'error'
        if (range.dismissed) dismissed = true
      }
    })
    if (!issueIds.length) continue

    const last = merged[merged.length - 1]
    if (
      last &&
      last.end === start &&
      last.severity === severity &&
      last.dismissed === dismissed &&
      last.issueIds.length === issueIds.length &&
      last.issueIds.every((id, i) => id === issueIds[i])
    ) {
      last.end = end
    } else {
      merged.push({ start, end, severity, dismissed, issueIds })
    }
  }

  return { merged, perIssue }
}

/** 解析 HTML，自验证定位所有 issue 并返回原子段 + per-issue 精确区间（可见文本 rune 坐标） */
export const collectHtmlHighlightRanges = (
  html: string,
  issues?: QualityIssue[],
): HtmlHighlightLayout => {
  if (!html || !issues?.length) return { merged: [], perIssue: issues?.map(() => null) ?? [] }
  const { runes } = buildVisibleTextMap(parseHtmlBody(html))
  return buildHtmlHighlightLayout(runes, issues)
}

/** 属性过滤：剔除 on* 事件属性与 javascript: 协议（DOM→VNode 不经过 v-html 消毒） */
const sanitizeElementProps = (el: Element): Record<string, string> => {
  const props: Record<string, string> = {}
  for (const attr of Array.from(el.attributes)) {
    const name = attr.name.toLowerCase()
    if (name.startsWith('on')) continue
    if ((name === 'href' || name === 'src') && /^\s*javascript:/i.test(attr.value)) continue
    props[attr.name] = attr.value
  }
  return props
}

/** 将文本节点按覆盖的原子段切分，命中部分包裹 <mark> */
const renderTextNodeWithHighlights = (
  node: Text,
  nodeStart: number,
  atoms: HtmlHighlightAtom[],
  activeIssueIndex: number | null,
): string | (string | VNode)[] => {
  const nodeRunes = Array.from(node.data)
  const nodeEnd = nodeStart + nodeRunes.length
  const overlapping = atoms.filter((a) => a.start < nodeEnd && a.end > nodeStart)
  if (!overlapping.length) return node.data

  const parts: (string | VNode)[] = []
  let cursor = 0
  for (const atom of overlapping) {
    const s = Math.max(atom.start, nodeStart) - nodeStart
    const e = Math.min(atom.end, nodeEnd) - nodeStart
    if (e <= s) continue
    if (s > cursor) parts.push(nodeRunes.slice(cursor, s).join(''))
    const isActive = activeIssueIndex != null && atom.issueIds.includes(activeIssueIndex)
    parts.push(
      h(
        'mark',
        {
          class: [
            'quality-span',
            atom.dismissed
              ? 'quality-span--dismissed'
              : atom.severity === 'error'
                ? 'quality-span--error'
                : 'quality-span--warning',
            isActive ? 'quality-span--active' : '',
          ],
          'data-issue-ids': atom.issueIds.join(','),
        },
        nodeRunes.slice(s, e).join(''),
      ),
    )
    cursor = e
  }
  if (cursor < nodeRunes.length) parts.push(nodeRunes.slice(cursor).join(''))
  return parts
}

interface DomToVNodeContext {
  nodeStarts: Map<Text, number>
  atoms: HtmlHighlightAtom[]
  activeIssueIndex: number | null
}

/** 递归将 DOM 节点转换为 VNode；危险标签整体剔除，文本节点按原子段包裹 <mark> */
const domNodeToVNode = (node: Node, ctx: DomToVNodeContext): string | VNode | null => {
  if (node.nodeType === Node.TEXT_NODE) {
    const textNode = node as Text
    const start = ctx.nodeStarts.get(textNode)
    if (start == null) return textNode.data || null
    const rendered = renderTextNodeWithHighlights(textNode, start, ctx.atoms, ctx.activeIssueIndex)
    if (typeof rendered === 'string') return rendered || null
    return h('span', null, rendered)
  }
  if (node.nodeType !== Node.ELEMENT_NODE) return null
  const el = node as Element
  const tag = el.tagName.toLowerCase()
  if (HTML_BLOCKED_TAGS.has(tag)) return null

  const children: (string | VNode)[] = []
  for (const child of Array.from(el.childNodes)) {
    const rendered = domNodeToVNode(child, ctx)
    if (rendered != null) children.push(rendered)
  }
  return h(tag, sanitizeElementProps(el), children)
}

/** 将桌面端共享 hover 键 `${segmentId}:${issueIndex}` 转为组件契约所需的数字索引 */
export const resolveActiveIssueIndex = (
  hoveredKey: string | null,
  segmentId: number,
): number | null => {
  if (!hoveredKey) return null
  const prefix = `${segmentId}:`
  if (!hoveredKey.startsWith(prefix)) return null
  const idx = Number(hoveredKey.slice(prefix.length))
  return Number.isFinite(idx) ? idx : null
}

/**
 * 将含 HTML 的目标文本渲染为带质量问题高亮的 VNode 树（HTML 模式）。
 * 高亮坐标基于可见文本 rune 偏移；activeIssueIndex 用于 hover/tap 联动强调。
 */
export const renderQualityHighlightedHtml = (
  html: string,
  issues?: QualityIssue[],
  activeIssueIndex: number | null = null,
  maxLines?: number,
): VNode => {
  const body = parseHtmlBody(html)
  const { runes, nodeStarts } = buildVisibleTextMap(body)
  const { merged } = buildHtmlHighlightLayout(runes, issues)

  const ctx: DomToVNodeContext = { nodeStarts, atoms: merged, activeIssueIndex }
  const children: (string | VNode)[] = []
  for (const child of Array.from(body.childNodes)) {
    const rendered = domNodeToVNode(child, ctx)
    if (rendered != null) children.push(rendered)
  }

  const style = maxLines
    ? {
        WebkitLineClamp: String(maxLines),
        display: '-webkit-box',
        WebkitBoxOrient: 'vertical' as const,
        overflow: 'hidden',
      }
    : undefined

  return h('div', { class: 'quality-html-content', style }, children)
}
