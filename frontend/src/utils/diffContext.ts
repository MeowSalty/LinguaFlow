import { buildTextContextWindows, hasOmittedText } from './textContext.ts'
import type { DiffTextPart } from './textDiff'

export interface DiffSnippetPart {
  type: DiffTextPart['type'] | 'ellipsis'
  text: string
}

export interface DiffSnippet {
  parts: DiffSnippetPart[]
  truncated: boolean
  totalChangeGroups: number
  visibleChangeGroups: number
  omittedChangeGroups: number
}

const append = (parts: DiffSnippetPart[], type: DiffSnippetPart['type'], text: string): void => {
  if (!text) return
  const previous = parts.at(-1)
  if (type !== 'ellipsis' && previous?.type === type) previous.text += text
  else parts.push({ type, text })
}

/** Clips unchanged context around diff hunks while keeping every displayed change intact. */
export const makeDiffSnippet = (source: DiffTextPart[], expanded = false): DiffSnippet => {
  const anchors: { start: number; end: number }[] = []
  let offset = 0
  let changeStart: number | null = null
  const flushAnchor = (): void => {
    if (changeStart !== null && offset > changeStart)
      anchors.push({ start: changeStart, end: offset })
    changeStart = null
  }

  for (const part of source) {
    const length = Array.from(part.text).length
    if (part.type === 'equal') flushAnchor()
    else changeStart ??= offset
    offset += length
  }
  flushAnchor()

  if (!anchors.length) {
    return {
      parts: source.map((part) => ({ ...part })),
      truncated: false,
      totalChangeGroups: 0,
      visibleChangeGroups: 0,
      omittedChangeGroups: 0,
    }
  }
  const runes = Array.from(source.map((part) => part.text).join(''))
  const windows = buildTextContextWindows(runes, anchors, expanded)
  const visibleAnchors = anchors.filter((anchor) =>
    windows.some((window) => anchor.start >= window.start && anchor.end <= window.end),
  )
  const visible: DiffSnippetPart[] = []
  let cursor = 0
  let sourceOffset = 0

  for (const window of windows) {
    if (window.start > cursor) append(visible, 'ellipsis', '…')
    for (const part of source) {
      const partLength = Array.from(part.text).length
      const partStart = sourceOffset
      const partEnd = partStart + partLength
      sourceOffset = partEnd
      const start = Math.max(partStart, window.start)
      const end = Math.min(partEnd, window.end)
      if (end > start) append(visible, part.type, runes.slice(start, end).join(''))
    }
    cursor = window.end
    sourceOffset = 0
  }
  if (cursor < runes.length) append(visible, 'ellipsis', '…')

  return {
    parts: visible,
    truncated: hasOmittedText(runes.length, windows),
    totalChangeGroups: anchors.length,
    visibleChangeGroups: visibleAnchors.length,
    omittedChangeGroups: anchors.length - visibleAnchors.length,
  }
}
