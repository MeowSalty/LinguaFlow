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

const toLowerRunes = (runes: string[]): string[] => runes.map((r) => r.toLowerCase())

/**
 * 在文本中收集全部搜索命中区间（rune 偏移，重叠合并）。
 * 区间语义与 collectQualityHighlightRanges 一致：Array.from 码点切分。
 */
export const buildSearchHighlightRanges = (
  text: string,
  query: string,
  caseSensitive = false,
): SearchHighlightRange[] => {
  if (!text || !query) return []

  const runes = Array.from(text)
  const needle = Array.from(query)
  if (!needle.length || needle.length > runes.length) return []

  const haystack = caseSensitive ? runes : toLowerRunes(runes)
  const needleKey = caseSensitive ? needle : toLowerRunes(needle)

  const ranges: SearchHighlightRange[] = []
  let from = 0
  while (from <= haystack.length - needleKey.length) {
    let matched = true
    for (let j = 0; j < needleKey.length; j++) {
      if (haystack[from + j] !== needleKey[j]) {
        matched = false
        break
      }
    }
    if (matched) {
      ranges.push({ start: from, end: from + needleKey.length })
      from += needleKey.length
    } else {
      from++
    }
  }

  return mergeSearchRanges(ranges)
}

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
 * 命中部分包裹 <mark class="search-hit">。
 */
export const renderSearchHighlightedText = (
  text: string,
  query: string,
  caseSensitive = false,
): VNode => {
  const ranges = buildSearchHighlightRanges(text, query, caseSensitive)
  if (!ranges.length) return h('span', null, text)

  const runes = Array.from(text)
  const children: (string | VNode)[] = []
  let cursor = 0
  for (const range of ranges) {
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
 * 命中坐标基于可见文本 rune 偏移（buildVisibleTextMap，与质量问题高亮同坐标系）。
 */
export const renderSearchHighlightedHtml = (
  html: string,
  query: string,
  caseSensitive = false,
  maxLines?: number,
): VNode => {
  const body = parseHtmlBody(html)
  const textMap: HtmlTextMap = buildVisibleTextMap(body)
  const ranges = buildSearchHighlightRanges(textMap.runes.join(''), query, caseSensitive)

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
 */
export const makeSearchSnippet = (
  text: string,
  query: string,
  caseSensitive = false,
  radius = 46,
): SearchSnippet => {
  const empty: SearchSnippet = { before: text, hit: '', after: '' }
  if (!text || !query) return empty

  const runes = Array.from(text)
  const ranges = buildSearchHighlightRanges(text, query, caseSensitive)
  const first = ranges[0]
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
