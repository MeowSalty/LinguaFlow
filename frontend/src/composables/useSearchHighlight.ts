import { RE2JS } from 're2js'
import type { VNode } from 'vue'
import { h } from 'vue'

import {
  buildVisibleTextMap,
  HTML_BLOCKED_TAGS,
  type HtmlTextMap,
  parseHtmlBody,
  sanitizeElementProps,
} from '@/composables/useQualityIssues'

/** 搜索命中区间（rune 偏移，与质量问题高亮管线同一坐标系） */
export interface SearchHighlightRange {
  start: number
  end: number
}

export interface SearchSnippet {
  /** 命中前的文本（超出截断半径时以省略号开头） */
  before: string
  /** 命中的关键词原文（保持原文大小写形态） */
  hit: string
  /** 命中后的文本（超出截断半径时以省略号结尾） */
  after: string
}

/** 展示级匹配模式，语义与后端 SegmentMatchMode 一致 */
export type SearchMatchMode = 'substring' | 'regex'

/** 展示级搜索匹配选项（与后端匹配参数同语义，供面板 / 主表高亮共用） */
export interface SearchMatchOptions {
  /** 区分大小写，默认 false */
  caseSensitive?: boolean
  /** 全字匹配：命中前后不得紧邻 Unicode 字母或数字，默认 false */
  wholeWord?: boolean
  /** 匹配模式：substring 字面子串（默认）/ regex 正则 */
  matchMode?: SearchMatchMode
}

/** 匹配选项的兼容形态：boolean 等价于 { caseSensitive }（历史调用签名） */
export type SearchMatchArgument = boolean | SearchMatchOptions

/**
 * 展示级匹配结果。
 * valid=false 表示 regex 无法编译或执行失败，ranges 保证为空；
 * 调用方应降级为无高亮（后端结果仍权威，不得改用字面或 JS 正则高亮）。
 */
export interface SearchMatchResult {
  ranges: SearchHighlightRange[]
  valid: boolean
}

interface NormalizedSearchMatchOptions {
  caseSensitive: boolean
  wholeWord: boolean
  matchMode: SearchMatchMode
}

const normalizeSearchMatchOptions = (
  options: SearchMatchArgument = false,
): NormalizedSearchMatchOptions => {
  if (typeof options === 'boolean') {
    return { caseSensitive: options, wholeWord: false, matchMode: 'substring' }
  }
  return {
    caseSensitive: options.caseSensitive ?? false,
    wholeWord: options.wholeWord ?? false,
    matchMode: options.matchMode ?? 'substring',
  }
}

/** 词边界判定用的 Unicode 字母与十进制数字（与后端 whole_word 的 IsLetter/IsDigit 一致） */
const WORD_CHARACTER_RE = /[\p{L}\p{Nd}]/u

const isWholeWordBoundary = (runes: string[], start: number, end: number): boolean => {
  const before = runes[start - 1]
  const after = runes[end]
  if (before && WORD_CHARACTER_RE.test(before)) return false
  if (after && WORD_CHARACTER_RE.test(after)) return false
  return true
}

/**
 * 把查询逐码点转义为 \u{...} 字面模式：用户字符不再具有正则元字符语义，
 * 任意输入都不会触发回溯，因此该模式不存在 ReDoS 风险。
 */
const escapeLiteralPattern = (query: string): string =>
  Array.from(query)
    .map((char) => `\\u{${(char.codePointAt(0) ?? 0).toString(16)}}`)
    .join('')

/** 各 rune 起点在 UTF-16 字符串中的下标（末位为文本总长） */
const buildRuneStarts = (runes: string[]): number[] => {
  const starts: number[] = []
  let offset = 0
  for (const rune of runes) {
    starts.push(offset)
    offset += rune.length
  }
  starts.push(offset)
  return starts
}

/** 二分查找：UTF-16 下标 → rune 偏移（RegExp 命中坐标系转高亮坐标系） */
const utf16IndexToRuneOffset = (runeStarts: number[], index: number): number => {
  let low = 0
  let high = runeStarts.length - 1
  while (low < high) {
    const mid = Math.ceil((low + high) / 2)
    if ((runeStarts[mid] ?? Number.POSITIVE_INFINITY) <= index) low = mid
    else high = mid - 1
  }
  return low
}

interface SubstringOptions {
  caseSensitive: boolean
  wholeWord: boolean
}

/**
 * 字面子串匹配：在转义后的字面 query 上使用安全正则（gu 区分大小写 / giu 不区分），
 * 大小写折叠由 Unicode simple fold 完成（与 Go strings.EqualFold 语义对齐，
 * 覆盖 Σ/ς、s/ſ 等情形）；命中 UTF-16 下标折算为 rune 区间后再做全字边界检查。
 */
const buildSubstringRanges = (
  text: string,
  query: string,
  { caseSensitive, wholeWord }: SubstringOptions,
): SearchHighlightRange[] => {
  const pattern = escapeLiteralPattern(query)
  if (!pattern) return []

  const matcher = new RegExp(pattern, caseSensitive ? 'gu' : 'giu')
  const runes = Array.from(text)
  const runeStarts = buildRuneStarts(runes)
  const ranges: SearchHighlightRange[] = []

  let match = matcher.exec(text)
  while (match !== null) {
    if (match[0].length === 0) {
      // 转义模式恒非空，防御性推进避免任何情况下的死循环
      matcher.lastIndex++
    } else {
      const start = utf16IndexToRuneOffset(runeStarts, match.index)
      const end = utf16IndexToRuneOffset(
        runeStarts,
        Math.min(match.index + match[0].length, text.length),
      )
      if (end > start && (!wholeWord || isWholeWordBoundary(runes, start, end))) {
        ranges.push({ start, end })
      } else if (wholeWord) {
        // 边界不通过的命中从其后一个 UTF-16 单元继续，避免漏掉重叠位置的合法命中
        matcher.lastIndex = match.index + 1
      }
    }
    match = matcher.exec(text)
  }

  return mergeSearchRanges(ranges)
}

interface RegexOptions {
  caseSensitive: boolean
  wholeWord: boolean
}

/** 正则编译结果缓存上限；缓存成功（RE2JS）与失败（null）结果，避免重复编译非法查询 */
const REGEX_CACHE_LIMIT = 64

/** 模块级 LRU：key 为最终 pattern（含大小写语义），按最近使用淘汰最旧项 */
const regexCache = new Map<string, RE2JS | null>()

const getOrCompileRegex = (pattern: string): RE2JS | null => {
  if (regexCache.has(pattern)) {
    const cached = regexCache.get(pattern) ?? null
    regexCache.delete(pattern)
    regexCache.set(pattern, cached)
    return cached
  }

  let compiled: RE2JS | null = null
  try {
    compiled = RE2JS.compile(pattern)
  } catch {
    compiled = null
  }

  if (regexCache.size >= REGEX_CACHE_LIMIT) {
    const oldest = regexCache.keys().next().value
    if (oldest !== undefined) regexCache.delete(oldest)
  }
  regexCache.set(pattern, compiled)
  return compiled
}

/**
 * 正则匹配：用户 pattern 交由 RE2（线性时间，无回溯）执行，与后端 Go regexp 的
 * FindAll 对齐——非重叠推进、零宽命中保留为零长区间。每个文本新建 matcher。
 * pattern 编译或执行异常时返回 valid=false，由调用方降级为无高亮。
 */
const buildRegexRanges = (
  text: string,
  query: string,
  { caseSensitive, wholeWord }: RegexOptions,
): SearchMatchResult => {
  const pattern = caseSensitive ? query : `(?i)${query}`
  const compiled = getOrCompileRegex(pattern)
  if (!compiled) return { ranges: [], valid: false }

  try {
    const runes = Array.from(text)
    const runeStarts = buildRuneStarts(runes)
    const ranges: SearchHighlightRange[] = []

    const matcher = compiled.matcher(text)
    while (matcher.find()) {
      const start = utf16IndexToRuneOffset(runeStarts, matcher.start())
      const end = utf16IndexToRuneOffset(runeStarts, matcher.end())
      // whole_word 后置过滤：拒绝后不回退重搜重叠位置，保持 FindAll 的非重叠推进
      if (wholeWord && !isWholeWordBoundary(runes, start, end)) continue
      ranges.push({ start, end })
    }

    return { ranges: mergeSearchRanges(ranges), valid: true }
  } catch {
    return { ranges: [], valid: false }
  }
}

/**
 * 计算文本中全部搜索命中（rune 偏移，重叠合并）并返回可用性状态。
 * substring 为转义字面匹配；regex 经 RE2 编译执行（无回溯，与后端 RE2 语义一致），
 * 非法或执行失败的 pattern 返回 { ranges: [], valid: false }，由调用方降级呈现。
 */
export const buildSearchMatch = (
  text: string,
  query: string,
  options: SearchMatchArgument = false,
): SearchMatchResult => {
  if (!text || !query) return { ranges: [], valid: true }

  const { caseSensitive, wholeWord, matchMode } = normalizeSearchMatchOptions(options)
  if (matchMode === 'regex') {
    return buildRegexRanges(text, query, { caseSensitive, wholeWord })
  }

  return { ranges: buildSubstringRanges(text, query, { caseSensitive, wholeWord }), valid: true }
}

/**
 * 兼容入口：只返回命中区间数组。
 * 第三参保持历史布尔语义（true = 区分大小写），也接受 SearchMatchOptions。
 */
export const buildSearchHighlightRanges = (
  text: string,
  query: string,
  options: SearchMatchArgument = false,
): SearchHighlightRange[] => buildSearchMatch(text, query, options).ranges

/** 合并相邻/重叠的命中区间（等宽区间无需 severity 仲裁，直接并集） */
const mergeSearchRanges = (ranges: SearchHighlightRange[]): SearchHighlightRange[] => {
  if (ranges.length <= 1) return ranges.map((r) => ({ ...r }))

  const sorted = [...ranges].sort((a, b) => a.start - b.start || a.end - b.end)
  const merged: SearchHighlightRange[] = []
  for (const range of sorted) {
    const last = merged[merged.length - 1]
    if (last && range.start <= last.end) {
      last.end = Math.max(last.end, range.end)
    } else {
      merged.push({ ...range })
    }
  }
  return merged
}

/**
 * 将文本渲染为带搜索命中高亮的 VNode（纯文本模式）。
 * 非零宽命中的部分包裹 <mark class="search-hit">；零宽命中不产生空 mark。
 */
export const renderSearchHighlightedText = (
  text: string,
  query: string,
  options: SearchMatchArgument = false,
): VNode => {
  const { ranges } = buildSearchMatch(text, query, options)
  if (!ranges.length) return h('span', null, text)

  const runes = Array.from(text)
  const children: (string | VNode)[] = []
  let cursor = 0
  for (const range of ranges) {
    if (range.end <= range.start) continue
    if (range.start > cursor) children.push(runes.slice(cursor, range.start).join(''))
    children.push(h('mark', { class: 'search-hit' }, runes.slice(range.start, range.end).join('')))
    cursor = range.end
  }
  if (cursor < runes.length) children.push(runes.slice(cursor).join(''))

  return h('span', { class: 'quality-highlighted-text' }, children)
}

// ── HTML 模式（复用质量问题的可见文本坐标体系）──

interface SearchDomContext {
  nodeStarts: Map<Text, number>
  ranges: SearchHighlightRange[]
}

/** 将文本节点按命中区间切分，命中部分包裹 <mark class="search-hit"> */
const renderSearchTextNode = (
  node: Text,
  nodeStart: number,
  ranges: SearchHighlightRange[],
): string | (string | VNode)[] => {
  const nodeRunes = Array.from(node.data)
  const nodeEnd = nodeStart + nodeRunes.length
  const overlapping = ranges.filter((r) => r.start < nodeEnd && r.end > nodeStart)
  if (!overlapping.length) return node.data

  const parts: (string | VNode)[] = []
  let cursor = 0
  for (const range of overlapping) {
    const s = Math.max(range.start, nodeStart) - nodeStart
    const e = Math.min(range.end, nodeEnd) - nodeStart
    if (e <= s) continue
    if (s > cursor) parts.push(nodeRunes.slice(cursor, s).join(''))
    parts.push(h('mark', { class: 'search-hit' }, nodeRunes.slice(s, e).join('')))
    cursor = e
  }
  if (cursor < nodeRunes.length) parts.push(nodeRunes.slice(cursor).join(''))
  return parts
}

/** 递归将 DOM 节点转换为 VNode；危险标签整体剔除，文本节点按命中区间包裹 <mark> */
const searchDomNodeToVNode = (node: Node, ctx: SearchDomContext): string | VNode | null => {
  if (node.nodeType === Node.TEXT_NODE) {
    const textNode = node as Text
    const start = ctx.nodeStarts.get(textNode)
    if (start == null) return textNode.data || null
    const rendered = renderSearchTextNode(textNode, start, ctx.ranges)
    if (typeof rendered === 'string') return rendered || null
    return h('span', null, rendered)
  }
  if (node.nodeType !== Node.ELEMENT_NODE) return null
  const el = node as Element
  const tag = el.tagName.toLowerCase()
  if (HTML_BLOCKED_TAGS.has(tag)) return null

  const children: (string | VNode)[] = []
  for (const child of Array.from(el.childNodes)) {
    const rendered = searchDomNodeToVNode(child, ctx)
    if (rendered != null) children.push(rendered)
  }
  return h(tag, sanitizeElementProps(el), children)
}

/**
 * 将含 HTML 的文本渲染为带搜索命中高亮的 VNode 树（HTML 模式）。
 * 命中坐标基于可见文本 rune 偏移（buildVisibleTextMap，与质量问题高亮同坐标系）；
 * 零宽命中不产生空 mark。
 */
export const renderSearchHighlightedHtml = (
  html: string,
  query: string,
  options: SearchMatchArgument = false,
  maxLines?: number,
): VNode => {
  const body = parseHtmlBody(html)
  const textMap: HtmlTextMap = buildVisibleTextMap(body)
  const { ranges } = buildSearchMatch(textMap.runes.join(''), query, options)

  const ctx: SearchDomContext = { nodeStarts: textMap.nodeStarts, ranges }
  const children: (string | VNode)[] = []
  for (const child of Array.from(body.childNodes)) {
    const rendered = searchDomNodeToVNode(child, ctx)
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

/**
 * 从文本中截取关键词居中的片段（搜索结果卡片用）。
 * 半径 radius 为命中前后各保留的 rune 数；避免在词中间切断。
 * 以首个非零宽命中为准；无可用命中（regex 非法或降级）时整段作为 before 返回，
 * 由调用方决定呈现。
 */
export const makeSearchSnippet = (
  text: string,
  query: string,
  options: SearchMatchArgument = false,
  radius = 46,
): SearchSnippet => {
  const empty: SearchSnippet = { before: text, hit: '', after: '' }
  if (!text || !query) return empty

  const runes = Array.from(text)
  const { ranges } = buildSearchMatch(text, query, options)
  const first = ranges.find((range) => range.end > range.start)
  if (!first) return empty

  let start = Math.max(0, first.start - radius)
  let end = Math.min(runes.length, first.end + radius + 14)
  // 收缩边界避免切断词中间（向词边界推进）
  while (start > 0 && /\S/.test(runes[start] ?? '')) start--
  while (end < runes.length && /\S/.test(runes[end - 1] ?? '')) end++

  return {
    before: (start > 0 ? '…' : '') + runes.slice(start, first.start).join(''),
    hit: runes.slice(first.start, first.end).join(''),
    after: runes.slice(first.end, end).join('') + (end < runes.length ? '…' : ''),
  }
}
