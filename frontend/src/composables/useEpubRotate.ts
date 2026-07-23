import { unzip, zipSync, strFromU8, strToU8 } from 'fflate'

export type Orientation = 'vertical' | 'horizontal'

/** auto: flip whole book from detected majority; otherwise force that orientation */
export type EpubRotateMode = 'auto' | Orientation

export type EpubRotateStage = 'idle' | 'parsing' | 'converting' | 'packing' | 'done' | 'error'

export interface EpubRotateStats {
  content: number
  styles: number
  /** Files rewritten because orientation differed from target */
  changed: number
  toVertical: number
  toHorizontal: number
  skipped: number
  alreadyTarget: number
  /** Content pages only (linked CSS used for detection, not counted as votes) */
  detectedVertical: number
  detectedHorizontal: number
}

export interface EpubRotateResult {
  blob: Blob
  filename: string
  stats: EpubRotateStats
  target: Orientation
  mode: EpubRotateMode
}

export interface EpubRotateProgress {
  stage: EpubRotateStage
  current: number
  total: number
}

/** Reject oversized uploads before inflate (client DoS guard). */
const MAX_EPUB_BYTES = 80 * 1024 * 1024
/** Cap single entry uncompressed size (zip bomb guard). */
const MAX_ENTRY_UNCOMPRESSED = 16 * 1024 * 1024
/** Cap total uncompressed payload retained in memory. */
const MAX_TOTAL_UNCOMPRESSED = 120 * 1024 * 1024
const MAX_ZIP_ENTRIES = 8000

const MARKER_ATTR = 'data-lf-orientation'
const STYLE_MARKER_START = '/* lf-orientation:start */'
const STYLE_MARKER_END = '/* lf-orientation:end */'

const VERTICAL_CSS =
  'html,body{writing-mode:vertical-rl;-webkit-writing-mode:vertical-rl;text-orientation:mixed;}'
const HORIZONTAL_CSS =
  'html,body{writing-mode:horizontal-tb;-webkit-writing-mode:horizontal-tb;text-orientation:mixed;}'

const XHTML_RE = /\.(xhtml|html|htm)$/i
const CSS_RE = /\.css$/i
const OPF_RE = /\.opf$/i

const WRITING_MODE_RE =
  /writing-mode\s*:\s*(vertical-rl|vertical-lr|horizontal-tb|sideways-rl|sideways-lr)/i
const MARKER_STYLE_RE =
  /<style[^>]*\bdata-lf-orientation\s*=\s*["']?(vertical|horizontal)["']?[^>]*>[\s\S]*?<\/style>/gi
const STYLE_BLOCK_RE = new RegExp(
  `${escapeRegExp(STYLE_MARKER_START)}[\\s\\S]*?${escapeRegExp(STYLE_MARKER_END)}\\n?`,
  'g',
)
const HEAD_CLOSE_RE = /<\/head>/i
const XML_ENCODING_RE = /<\?xml[^>]*encoding\s*=\s*["']([^"']+)["']/i
const LINK_HREF_RE =
  /<link[^>]+rel\s*=\s*["']stylesheet["'][^>]*href\s*=\s*["']([^"']+)["'][^>]*>/gi
const LINK_HREF_RE_ALT =
  /<link[^>]+href\s*=\s*["']([^"']+)["'][^>]*rel\s*=\s*["']stylesheet["'][^>]*>/gi
const CONTAINER_ROOTFILE_RE = /full-path\s*=\s*["']([^"']+\.opf)["']/i
const MANIFEST_ITEM_RE =
  /<item\b[^>]*\bid\s*=\s*["']([^"']+)["'][^>]*\bhref\s*=\s*["']([^"']+)["'][^>]*(?:\bmedia-type\s*=\s*["']([^"']+)["'])?[^>]*\/?>/gi
const MANIFEST_ITEM_RE_ALT =
  /<item\b[^>]*\bhref\s*=\s*["']([^"']+)["'][^>]*\bid\s*=\s*["']([^"']+)["'][^>]*(?:\bmedia-type\s*=\s*["']([^"']+)["'])?[^>]*\/?>/gi

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

function normalizePath(path: string): string {
  return path.replace(/\\/g, '/').replace(/^\.\//, '')
}

function dirname(path: string): string {
  const n = normalizePath(path)
  const i = n.lastIndexOf('/')
  return i >= 0 ? n.slice(0, i) : ''
}

function resolvePath(baseDir: string, href: string): string {
  const clean = href.split('#')[0]?.split('?')[0] ?? href
  if (
    !clean ||
    clean.startsWith('http://') ||
    clean.startsWith('https://') ||
    clean.startsWith('data:')
  ) {
    return ''
  }
  const parts = [...(baseDir ? baseDir.split('/') : []), ...clean.split('/')]
  const stack: string[] = []
  for (const p of parts) {
    if (!p || p === '.') continue
    if (p === '..') {
      stack.pop()
    } else {
      stack.push(p)
    }
  }
  return stack.join('/')
}

function decodeText(data: Uint8Array): string {
  const head = strFromU8(data.subarray(0, Math.min(data.length, 200)))
  const encMatch = head.match(XML_ENCODING_RE)
  const encoding = encMatch?.[1]?.toLowerCase() || 'utf-8'
  try {
    return new TextDecoder(encoding).decode(data)
  } catch {
    return strFromU8(data)
  }
}

function isVerticalMode(value: string | null | undefined): boolean {
  if (!value) return false
  const v = value.toLowerCase()
  return v.startsWith('vertical') || v.startsWith('sideways')
}

function detectOrientationInText(text: string): Orientation | null {
  const marker = text.match(/data-lf-orientation\s*=\s*["']?(vertical|horizontal)["']?/i)
  if (marker?.[1]) {
    return marker[1].toLowerCase() === 'vertical' ? 'vertical' : 'horizontal'
  }
  if (text.includes(`${STYLE_MARKER_START}`) && /writing-mode\s*:\s*vertical/i.test(text)) {
    return 'vertical'
  }
  if (text.includes(`${STYLE_MARKER_START}`) && /writing-mode\s*:\s*horizontal/i.test(text)) {
    return 'horizontal'
  }
  const modes = [...text.matchAll(new RegExp(WRITING_MODE_RE.source, 'gi'))]
  for (const m of modes) {
    if (isVerticalMode(m[1])) return 'vertical'
  }
  for (const m of modes) {
    if (m[1]?.toLowerCase() === 'horizontal-tb') return 'horizontal'
  }
  return null
}

function detectOrientation(text: string): Orientation {
  return detectOrientationInText(text) ?? 'horizontal'
}

function detectContentOrientation(xhtml: string, linkedCssTexts: string[]): Orientation {
  const own = detectOrientationInText(xhtml)
  if (own) return own
  for (const css of linkedCssTexts) {
    const o = detectOrientationInText(css)
    if (o === 'vertical') return 'vertical'
  }
  for (const css of linkedCssTexts) {
    const o = detectOrientationInText(css)
    if (o === 'horizontal') return 'horizontal'
  }
  return 'horizontal'
}

function stripInjected(text: string): string {
  return text.replace(MARKER_STYLE_RE, '').replace(STYLE_BLOCK_RE, '')
}

function injectXhtml(text: string, target: Orientation): { text: string; ok: boolean } {
  const cleaned = stripInjected(text)
  const css = target === 'vertical' ? VERTICAL_CSS : HORIZONTAL_CSS
  const block = `<style ${MARKER_ATTR}="${target}">${css}</style>`
  if (!HEAD_CLOSE_RE.test(cleaned)) {
    return { text: cleaned, ok: false }
  }
  return { text: cleaned.replace(HEAD_CLOSE_RE, `${block}\n</head>`), ok: true }
}

function injectCss(text: string, target: Orientation): string {
  const cleaned = stripInjected(text).replace(/\s*$/, '')
  const css = target === 'vertical' ? VERTICAL_CSS : HORIZONTAL_CSS
  return `${cleaned}\n${STYLE_MARKER_START}\n${css}\n${STYLE_MARKER_END}\n`
}

function buildPathIndex(files: Record<string, Uint8Array>): Map<string, string> {
  const index = new Map<string, string>()
  for (const key of Object.keys(files)) {
    index.set(normalizePath(key), key)
  }
  return index
}

function findFileKey(
  files: Record<string, Uint8Array>,
  path: string,
  pathIndex?: Map<string, string>,
): string | null {
  const target = normalizePath(path)
  if (files[target]) return target
  if (pathIndex) return pathIndex.get(target) ?? null
  return Object.keys(files).find((k) => normalizePath(k) === target) ?? null
}

function findOpfPath(
  files: Record<string, Uint8Array>,
  pathIndex: Map<string, string>,
): string | null {
  let containerKey: string | undefined
  for (const [norm, key] of pathIndex) {
    if (norm.toLowerCase() === 'meta-inf/container.xml') {
      containerKey = key
      break
    }
  }
  if (containerKey) {
    const xml = decodeText(files[containerKey]!)
    const m = xml.match(CONTAINER_ROOTFILE_RE)
    if (m?.[1]) return normalizePath(m[1])
  }
  const opfKey = Object.keys(files).find(
    (k) => OPF_RE.test(k) && !normalizePath(k).toLowerCase().startsWith('meta-inf/'),
  )
  return opfKey ? normalizePath(opfKey) : null
}

function collectFromOpf(
  files: Record<string, Uint8Array>,
  opfPath: string,
  pathIndex: Map<string, string>,
): { content: Set<string>; styles: Set<string> } {
  const content = new Set<string>()
  const styles = new Set<string>()
  const opfKey = findFileKey(files, opfPath, pathIndex)
  const opfData = opfKey ? files[opfKey] : undefined
  if (!opfData) return { content, styles }

  const opfText = decodeText(opfData)
  const opfDir = dirname(opfPath)
  const seen = new Set<string>()
  const items: Array<{ href: string; mediaType: string }> = []

  const pushItem = (href: string | undefined, mediaType: string | undefined): void => {
    if (!href || seen.has(href)) return
    seen.add(href)
    items.push({ href, mediaType: (mediaType ?? '').toLowerCase() })
  }

  MANIFEST_ITEM_RE.lastIndex = 0
  let m: RegExpExecArray | null
  while ((m = MANIFEST_ITEM_RE.exec(opfText)) !== null) {
    pushItem(m[2], m[3])
  }
  MANIFEST_ITEM_RE_ALT.lastIndex = 0
  while ((m = MANIFEST_ITEM_RE_ALT.exec(opfText)) !== null) {
    pushItem(m[1], m[3])
  }

  for (const item of items) {
    const path = resolvePath(opfDir, item.href)
    if (!path) continue
    const fileKey = findFileKey(files, path, pathIndex)
    if (!fileKey) continue
    if (
      item.mediaType === 'application/xhtml+xml' ||
      item.mediaType === 'text/html' ||
      XHTML_RE.test(path)
    ) {
      content.add(fileKey)
    } else if (item.mediaType === 'text/css' || CSS_RE.test(path)) {
      styles.add(fileKey)
    }
  }

  return { content, styles }
}

function collectFallback(files: Record<string, Uint8Array>): {
  content: Set<string>
  styles: Set<string>
} {
  const content = new Set<string>()
  const styles = new Set<string>()
  for (const key of Object.keys(files)) {
    const n = normalizePath(key)
    if (n.toLowerCase().startsWith('meta-inf/')) continue
    if (XHTML_RE.test(n)) content.add(key)
    else if (CSS_RE.test(n)) styles.add(key)
  }
  return { content, styles }
}

/** Shared stylesheet link walk for collect + detect. */
function listLinkedStyleKeys(
  files: Record<string, Uint8Array>,
  contentKey: string,
  pathIndex: Map<string, string>,
  xhtmlText?: string,
): string[] {
  const data = files[contentKey]
  if (!data && !xhtmlText) return []
  const text = xhtmlText ?? decodeText(data!)
  const base = dirname(normalizePath(contentKey))
  const keys: string[] = []
  const seen = new Set<string>()
  for (const re of [LINK_HREF_RE, LINK_HREF_RE_ALT]) {
    re.lastIndex = 0
    let m: RegExpExecArray | null
    while ((m = re.exec(text)) !== null) {
      const resolved = resolvePath(base, m[1] ?? '')
      if (!resolved) continue
      const fileKey = findFileKey(files, resolved, pathIndex)
      if (fileKey && CSS_RE.test(fileKey) && !seen.has(fileKey)) {
        seen.add(fileKey)
        keys.push(fileKey)
      }
    }
  }
  return keys
}

function collectLinkedStyles(
  files: Record<string, Uint8Array>,
  contentPaths: Set<string>,
  styles: Set<string>,
  pathIndex: Map<string, string>,
): void {
  for (const key of contentPaths) {
    for (const fileKey of listLinkedStyleKeys(files, key, pathIndex)) {
      styles.add(fileKey)
    }
  }
}

function buildFilename(originalName: string, target: Orientation): string {
  const base = originalName.replace(/\.epub$/i, '')
  const suffix = target === 'vertical' ? '-v' : '-h'
  const cleaned = base.replace(/-(h|v)$/i, '')
  return `${cleaned}${suffix}.epub`
}

function resolveBookTarget(
  mode: EpubRotateMode,
  detectedVertical: number,
  detectedHorizontal: number,
): Orientation {
  if (mode === 'vertical' || mode === 'horizontal') return mode
  if (detectedVertical >= detectedHorizontal) return 'horizontal'
  return 'vertical'
}

async function unzipEpubGuarded(buffer: Uint8Array): Promise<Record<string, Uint8Array>> {
  let entryCount = 0
  let totalUncompressed = 0
  let limitError: Error | null = null

  return new Promise((resolve, reject) => {
    unzip(
      buffer,
      {
        filter: (file) => {
          if (limitError) return false
          entryCount++
          if (entryCount > MAX_ZIP_ENTRIES) {
            limitError = new Error('tooLarge')
            return false
          }
          const size = file.originalSize ?? 0
          if (size > MAX_ENTRY_UNCOMPRESSED) {
            limitError = new Error('tooLarge')
            return false
          }
          totalUncompressed += size
          if (totalUncompressed > MAX_TOTAL_UNCOMPRESSED) {
            limitError = new Error('tooLarge')
            return false
          }
          return true
        },
      },
      (err, data) => {
        if (limitError) reject(limitError)
        else if (err) reject(err)
        else resolve(data)
      },
    )
  })
}

export async function convertEpubOrientation(
  file: File,
  mode: EpubRotateMode = 'auto',
  onProgress?: (p: EpubRotateProgress) => void,
): Promise<EpubRotateResult> {
  if (!file.name.toLowerCase().endsWith('.epub')) {
    throw new Error('notEpub')
  }
  if (file.size > MAX_EPUB_BYTES) {
    throw new Error('tooLarge')
  }

  onProgress?.({ stage: 'parsing', current: 0, total: 0 })

  const buffer = new Uint8Array(await file.arrayBuffer())
  let files: Record<string, Uint8Array>
  try {
    files = await unzipEpubGuarded(buffer)
  } catch (e) {
    if (e instanceof Error && e.message === 'tooLarge') throw e
    throw new Error('parseFailed')
  }

  const pathIndex = buildPathIndex(files)
  const opfPath = findOpfPath(files, pathIndex)
  let content: Set<string>
  let styles: Set<string>
  if (opfPath) {
    ;({ content, styles } = collectFromOpf(files, opfPath, pathIndex))
  } else {
    ;({ content, styles } = collectFallback(files))
  }
  if (content.size === 0 && styles.size === 0) {
    ;({ content, styles } = collectFallback(files))
  }
  collectLinkedStyles(files, content, styles, pathIndex)

  if (content.size === 0 && styles.size === 0) {
    throw new Error('noContent')
  }

  const textCache = new Map<string, string>()
  const getText = (key: string): string => {
    const cached = textCache.get(key)
    if (cached !== undefined) return cached
    const data = files[key]
    const text = data ? decodeText(data) : ''
    textCache.set(key, text)
    return text
  }

  // Majority from content pages only; linked CSS informs each page, no style file votes
  let detectedVertical = 0
  let detectedHorizontal = 0
  const contentOrientation = new Map<string, Orientation>()
  for (const key of content) {
    if (!files[key]) continue
    const xhtml = getText(key)
    const linked = listLinkedStyleKeys(files, key, pathIndex, xhtml).map(getText)
    const current = detectContentOrientation(xhtml, linked)
    contentOrientation.set(key, current)
    if (current === 'vertical') detectedVertical++
    else detectedHorizontal++
  }

  const target = resolveBookTarget(mode, detectedVertical, detectedHorizontal)

  const stats: EpubRotateStats = {
    content: content.size,
    styles: styles.size,
    changed: 0,
    toVertical: 0,
    toHorizontal: 0,
    skipped: 0,
    alreadyTarget: 0,
    detectedVertical,
    detectedHorizontal,
  }

  const workList = [...content, ...styles]
  const total = workList.length
  onProgress?.({ stage: 'converting', current: 0, total })

  const modified: Record<string, Uint8Array> = { ...files }

  let i = 0
  for (const key of content) {
    i++
    onProgress?.({ stage: 'converting', current: i, total })
    const data = files[key]
    if (!data) {
      stats.skipped++
      continue
    }
    const text = getText(key)
    const current = contentOrientation.get(key) ?? detectOrientation(text)
    const { text: next, ok } = injectXhtml(text, target)
    if (!ok) {
      stats.skipped++
      continue
    }
    if (current === target) {
      stats.alreadyTarget++
    } else {
      stats.changed++
      if (target === 'vertical') stats.toVertical++
      else stats.toHorizontal++
    }
    modified[key] = strToU8(next)
  }

  for (const key of styles) {
    i++
    onProgress?.({ stage: 'converting', current: i, total })
    const data = files[key]
    if (!data) {
      stats.skipped++
      continue
    }
    const text = getText(key)
    const current = detectOrientation(text)
    const next = injectCss(text, target)
    if (current === target) {
      stats.alreadyTarget++
    } else {
      stats.changed++
      if (target === 'vertical') stats.toVertical++
      else stats.toHorizontal++
    }
    modified[key] = strToU8(next)
  }

  onProgress?.({ stage: 'packing', current: total, total })

  const zipInput: Record<string, Uint8Array | [Uint8Array, { level: 0 | 6 }]> = {}
  const mimetypeKey =
    pathIndex.get('mimetype') ?? Object.keys(modified).find((k) => normalizePath(k) === 'mimetype')
  if (mimetypeKey && modified[mimetypeKey]) {
    zipInput.mimetype = [modified[mimetypeKey]!, { level: 0 }]
  }
  for (const [key, data] of Object.entries(modified)) {
    if (mimetypeKey && key === mimetypeKey) continue
    zipInput[key] = [data, { level: 6 }]
  }

  const zipped = zipSync(zipInput)
  const blob = new Blob([zipped], { type: 'application/epub+zip' })
  const filename = buildFilename(file.name, target)

  onProgress?.({ stage: 'done', current: total, total })

  return { blob, filename, stats, target, mode }
}

export function useEpubRotate() {
  const stage = ref<EpubRotateStage>('idle')
  const progress = ref({ current: 0, total: 0 })
  const result = ref<EpubRotateResult | null>(null)
  const error = ref<string | null>(null)
  const busy = computed(() => ['parsing', 'converting', 'packing'].includes(stage.value))

  const convert = async (
    file: File,
    mode: EpubRotateMode = 'auto',
  ): Promise<EpubRotateResult | null> => {
    stage.value = 'parsing'
    progress.value = { current: 0, total: 0 }
    result.value = null
    error.value = null
    try {
      const res = await convertEpubOrientation(file, mode, (p) => {
        stage.value = p.stage
        progress.value = { current: p.current, total: p.total }
      })
      result.value = res
      stage.value = 'done'
      return res
    } catch (e) {
      stage.value = 'error'
      const msg = e instanceof Error ? e.message : 'convertFailed'
      error.value = msg
      return null
    }
  }

  const reset = (): void => {
    stage.value = 'idle'
    progress.value = { current: 0, total: 0 }
    result.value = null
    error.value = null
  }

  const downloadResult = (): void => {
    if (!result.value) return
    const url = URL.createObjectURL(result.value.blob)
    const a = document.createElement('a')
    a.href = url
    a.download = result.value.filename
    a.click()
    URL.revokeObjectURL(url)
  }

  return {
    stage,
    progress,
    result,
    error,
    busy,
    convert,
    reset,
    downloadResult,
  }
}
