export interface TextContextAnchor {
  start: number
  end: number
}

export interface TextContextWindow {
  start: number
  end: number
}

export interface TextContextPart {
  type: 'text' | 'hit' | 'ellipsis'
  text: string
}

export interface TextContextSnippet {
  parts: TextContextPart[]
  visibleAnchorCount: number
  truncated: boolean
}

const CONTEXT_BEFORE = 5
const CONTEXT_AFTER = 15
const WORD_BOUNDARY_EXTRA = 5
const MAX_CONTEXT_WINDOW_LENGTH = 220
const MAX_CONTEXT_GROUPS = 3
const MERGE_GAP = 32

/** Builds bounded context windows without cutting through a match. */
export const buildTextContextWindows = (
  text: string[],
  anchors: TextContextAnchor[],
  expanded = false,
): TextContextWindow[] => {
  if (!text.length) return []
  if (expanded || text.length <= MAX_CONTEXT_WINDOW_LENGTH || !anchors.length) {
    return [{ start: 0, end: text.length }]
  }

  const candidates = anchors
    .filter((anchor) => anchor.end > anchor.start)
    .map((anchor) => {
      let start = Math.max(0, anchor.start - CONTEXT_BEFORE)
      let end = Math.min(text.length, anchor.end + CONTEXT_AFTER)
      let startExtra = WORD_BOUNDARY_EXTRA
      while (startExtra > 0 && start > 0 && /\S/.test(text[start] ?? '')) {
        start--
        startExtra--
      }
      let endExtra = WORD_BOUNDARY_EXTRA
      while (endExtra > 0 && end < text.length && /\S/.test(text[end - 1] ?? '')) {
        end++
        endExtra--
      }
      return { start, end }
    })

  const merged: TextContextWindow[] = []
  for (const candidate of candidates) {
    const previous = merged.at(-1)
    if (!previous) {
      merged.push(candidate)
      continue
    }

    const gap = candidate.start - previous.end
    const mergedLength = candidate.end - previous.start
    if (gap <= MERGE_GAP || mergedLength <= MAX_CONTEXT_WINDOW_LENGTH) {
      previous.end = Math.max(previous.end, candidate.end)
    } else {
      merged.push(candidate)
    }
  }

  return merged.slice(0, MAX_CONTEXT_GROUPS)
}

export const hasOmittedText = (textLength: number, windows: TextContextWindow[]): boolean => {
  if (!windows.length) return textLength > 0
  return windows.length > 1 || windows[0]!.start > 0 || windows.at(-1)!.end < textLength
}

export const makeTextContextSnippet = (
  text: string,
  anchors: TextContextAnchor[],
  expanded = false,
): TextContextSnippet => {
  const runes = Array.from(text)
  const visibleAnchors = anchors.filter((anchor) => anchor.end > anchor.start)
  if (!visibleAnchors.length) {
    return {
      parts: text ? [{ type: 'text', text }] : [],
      visibleAnchorCount: 0,
      truncated: false,
    }
  }

  const windows = buildTextContextWindows(runes, visibleAnchors, expanded)
  const displayedAnchors = visibleAnchors.filter((anchor) =>
    windows.some((window) => anchor.start >= window.start && anchor.end <= window.end),
  )
  const parts: TextContextPart[] = []
  const append = (type: TextContextPart['type'], value: string): void => {
    if (!value) return
    const previous = parts.at(-1)
    if (type !== 'ellipsis' && previous?.type === type) previous.text += value
    else parts.push({ type, text: value })
  }

  let cursor = 0
  for (const window of windows) {
    if (window.start > cursor) append('ellipsis', '…')
    let textCursor = window.start
    for (const anchor of displayedAnchors) {
      if (anchor.start < window.start || anchor.end > window.end) continue
      if (anchor.start > textCursor) append('text', runes.slice(textCursor, anchor.start).join(''))
      append('hit', runes.slice(anchor.start, anchor.end).join(''))
      textCursor = anchor.end
    }
    if (window.end > textCursor) append('text', runes.slice(textCursor, window.end).join(''))
    cursor = window.end
  }
  if (cursor < runes.length) append('ellipsis', '…')

  return {
    parts,
    visibleAnchorCount: displayedAnchors.length,
    truncated: hasOmittedText(runes.length, windows),
  }
}
