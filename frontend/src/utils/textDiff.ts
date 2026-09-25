import type { SearchHighlightRange } from '@/composables/useSearchHighlight'

export interface DiffTextPart {
  type: 'equal' | 'delete' | 'insert'
  text: string
}

type Edit = { type: 'equal' | 'delete' | 'insert'; before: number; after: number }

const MAX_EDIT_DISTANCE = 96

const appendPart = (parts: DiffTextPart[], type: DiffTextPart['type'], text: string): void => {
  if (!text) return
  const previous = parts.at(-1)
  if (previous?.type === type) previous.text += text
  else parts.push({ type, text })
}

const coarseDiffParts = (before: string[], after: string[]): DiffTextPart[] => {
  let prefix = 0
  while (prefix < before.length && prefix < after.length && before[prefix] === after[prefix])
    prefix++

  let suffix = 0
  while (
    suffix < before.length - prefix &&
    suffix < after.length - prefix &&
    before[before.length - suffix - 1] === after[after.length - suffix - 1]
  )
    suffix++

  const beforeEnd = before.length - suffix
  const afterEnd = after.length - suffix
  const parts: DiffTextPart[] = []
  appendPart(parts, 'equal', before.slice(0, prefix).join(''))
  appendPart(parts, 'delete', before.slice(prefix, beforeEnd).join(''))
  appendPart(parts, 'insert', after.slice(prefix, afterEnd).join(''))
  appendPart(parts, 'equal', before.slice(beforeEnd).join(''))
  return parts
}

/** Maps literal search matches to their exact replacement spans when the API result agrees. */
export const mapReplacementParts = (
  beforeText: string,
  afterText: string,
  matchRanges: SearchHighlightRange[],
  replacement: string,
): DiffTextPart[] | null => {
  if (!matchRanges.length) return null

  const before = Array.from(beforeText)
  const output: string[] = []
  const parts: DiffTextPart[] = []
  let cursor = 0

  for (const range of matchRanges) {
    if (range.start < cursor || range.end > before.length) return null
    const unchanged = before.slice(cursor, range.start)
    output.push(...unchanged)
    appendPart(parts, 'equal', unchanged.join(''))

    const removed = before.slice(range.start, range.end)
    output.push(...Array.from(replacement))
    appendPart(parts, 'delete', removed.join(''))
    appendPart(parts, 'insert', replacement)
    cursor = range.end
  }

  const unchanged = before.slice(cursor)
  output.push(...unchanged)
  appendPart(parts, 'equal', unchanged.join(''))
  return output.join('') === afterText ? parts : null
}

/** Returns an inline diff in code-point order, grouping each removal before its insertion. */
export const diffTextParts = (beforeText: string, afterText: string): DiffTextPart[] => {
  const before = Array.from(beforeText)
  const after = Array.from(afterText)
  if (beforeText === afterText) return before.length ? [{ type: 'equal', text: beforeText }] : []

  const trace: Map<number, number>[] = []
  const furthest = new Map<number, number>([[1, 0]])
  let distance = -1

  for (let d = 0; d <= MAX_EDIT_DISTANCE && d <= before.length + after.length; d++) {
    for (let k = -d; k <= d; k += 2) {
      let x =
        k === -d || (k !== d && (furthest.get(k - 1) ?? -1) < (furthest.get(k + 1) ?? -1))
          ? (furthest.get(k + 1) ?? 0)
          : (furthest.get(k - 1) ?? 0) + 1
      let y = x - k

      while (x < before.length && y < after.length && before[x] === after[y]) {
        x++
        y++
      }
      furthest.set(k, x)

      if (x >= before.length && y >= after.length) {
        distance = d
        break
      }
    }
    trace.push(new Map(furthest))
    if (distance >= 0) break
  }

  if (distance < 0) return coarseDiffParts(before, after)

  const edits: Edit[] = []
  let x = before.length
  let y = after.length
  for (let d = distance; d > 0; d--) {
    const previous = trace[d - 1]!
    const k = x - y
    const previousK =
      k === -d || (k !== d && (previous.get(k - 1) ?? -1) < (previous.get(k + 1) ?? -1))
        ? k + 1
        : k - 1
    const previousX = previous.get(previousK) ?? 0
    const previousY = previousX - previousK

    while (x > previousX && y > previousY) {
      edits.push({ type: 'equal', before: x - 1, after: y - 1 })
      x--
      y--
    }

    if (x === previousX) {
      edits.push({ type: 'insert', before: x, after: y - 1 })
      y--
    } else {
      edits.push({ type: 'delete', before: x - 1, after: y })
      x--
    }
  }
  while (x > 0 && y > 0) {
    edits.push({ type: 'equal', before: x - 1, after: y - 1 })
    x--
    y--
  }
  while (x > 0) edits.push({ type: 'delete', before: --x, after: 0 })
  while (y > 0) edits.push({ type: 'insert', before: 0, after: --y })
  edits.reverse()

  const parts: DiffTextPart[] = []
  let removed: string[] = []
  let inserted: string[] = []
  const flushChanges = (): void => {
    appendPart(parts, 'delete', removed.join(''))
    appendPart(parts, 'insert', inserted.join(''))
    removed = []
    inserted = []
  }

  for (const edit of edits) {
    if (edit.type === 'equal') {
      flushChanges()
      appendPart(parts, 'equal', before[edit.before] ?? '')
    } else if (edit.type === 'delete') {
      removed.push(before[edit.before] ?? '')
    } else {
      inserted.push(after[edit.after] ?? '')
    }
  }
  flushChanges()
  return parts
}

/** Returns changed spans in Unicode code-point offsets, with a bounded fallback for large rewrites. */
export const diffTextRanges = (
  beforeText: string,
  afterText: string,
): { before: SearchHighlightRange[]; after: SearchHighlightRange[] } => {
  const parts = diffTextParts(beforeText, afterText)
  const before: SearchHighlightRange[] = []
  const after: SearchHighlightRange[] = []
  let beforeOffset = 0
  let afterOffset = 0
  for (const part of parts) {
    const length = Array.from(part.text).length
    if (part.type === 'delete') before.push({ start: beforeOffset, end: beforeOffset + length })
    if (part.type === 'insert') after.push({ start: afterOffset, end: afterOffset + length })
    if (part.type !== 'insert') beforeOffset += length
    if (part.type !== 'delete') afterOffset += length
  }
  return { before, after }
}

/** Maps diff parts back to legacy before/after ranges. */
export const mapReplacementRanges = (
  beforeText: string,
  afterText: string,
  matchRanges: SearchHighlightRange[],
  replacement: string,
): { before: SearchHighlightRange[]; after: SearchHighlightRange[] } | null => {
  const parts = mapReplacementParts(beforeText, afterText, matchRanges, replacement)
  if (!parts) return null
  const before: SearchHighlightRange[] = []
  const after: SearchHighlightRange[] = []
  let beforeOffset = 0
  let afterOffset = 0
  for (const part of parts) {
    const length = Array.from(part.text).length
    if (part.type === 'delete') before.push({ start: beforeOffset, end: beforeOffset + length })
    if (part.type === 'insert') after.push({ start: afterOffset, end: afterOffset + length })
    if (part.type !== 'insert') beforeOffset += length
    if (part.type !== 'delete') afterOffset += length
  }
  return { before, after }
}
