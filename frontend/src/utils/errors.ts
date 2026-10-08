export interface ApiProblem {
  status?: number
  title?: string
  detail?: string
}

/**
 * 从未知错误对象中提取用户可读的消息：
 * Error 实例取 message，否则按 Problem Details 结构取 detail/title，最后回退到 fallback。
 */
export const extractErrorMessage = (error: unknown, fallback: string): string => {
  if (error instanceof Error && error.message) {
    return error.message
  }
  if (error && typeof error === 'object') {
    const problem = error as ApiProblem
    return problem.detail || problem.title || fallback
  }
  return fallback
}

/** 从错误文本中抠出首个 JSON 对象并解析；解析失败或不是对象时返回 null。 */
const extractJsonPayload = (raw: string): Record<string, unknown> | null => {
  const match = /\{[\s\S]*\}/u.exec(raw)
  if (!match?.[0]) {
    return null
  }

  try {
    const parsed: unknown = JSON.parse(match[0])
    return parsed && typeof parsed === 'object' ? (parsed as Record<string, unknown>) : null
  } catch {
    return null
  }
}

/** 读取 JSON 错误体中的消息字段，兼容 `{message}` 与 `{error: {message}}` 两种上游形态。 */
const extractPayloadMessage = (payload: Record<string, unknown>): string | null => {
  const read = (value: unknown): string | null =>
    typeof value === 'string' && value.trim() ? value : null

  const nestedError =
    payload.error && typeof payload.error === 'object'
      ? (payload.error as Record<string, unknown>)
      : null

  return (
    read(payload.message) ??
    read(payload.error) ??
    read(nestedError?.message) ??
    read(payload.detail)
  )
}

/** 包装层剥离规则：`to` 为 '$1' 表示成对剥掉开闭括号并保留中间内容，否则删除匹配片段。 */
const PROBE_DETAIL_PEELS: Array<{ pattern: RegExp; to: string }> = [
  // 后端包装层 `拉取模型列表失败 (上游返回 …)`，文字与括号间可能有空格
  { pattern: /^拉取模型列表失败\s*[（(](.*)[）)]$/u, to: '$1' },
  { pattern: /^listBackendModelsFailed\s*[（(](.*)[）)]$/u, to: '$1' },
  // 没有闭括号的裸包装文案（如上游 5xx 时后端只返回固定文案）
  { pattern: /^拉取模型列表失败\s*[（(]?\s*/u, to: '' },
  { pattern: /^listBackendModelsFailed\s*[（(]?\s*/u, to: '' },
  { pattern: /^[（(]?\s*上游返回\s*/u, to: '' },
  // 只剥“状态码:”形态，保留 `405 Method Not Allowed` 里的状态码
  { pattern: /^\d{3}(?=\s*[:：])/u, to: '' },
  // SDK 传输错误前缀 `GET "…": `
  { pattern: /^(?:GET|HEAD|POST|PUT|DELETE|PATCH|OPTIONS)\s*(?=["':：]|$)/iu, to: '' },
  { pattern: /^[:：]\s*/u, to: '' },
]

/**
 * 清洗探测模型列表失败的错误细节，用于通知正文：
 * 剥掉后端包装层与 SDK 传输错误碎片，并脱敏 URL、JSON 体与密钥。
 * 返回空字符串表示没有可展示的细节。
 */
export const sanitizeProbeErrorDetail = (raw: string): string => {
  const payload = extractJsonPayload(raw)
  const payloadMessage = payload ? extractPayloadMessage(payload) : null

  let detail = (payloadMessage ?? raw)
    .replace(/https?:\/\/[^\s"'）)]+/giu, '')
    .replace(/\s*\{[\s\S]*\}\s*/gu, ' ')
    .replace(/\b(?:api[_-]?key|token|authorization)\s*[:=]\s*\S+/giu, '')
    // `token "sk-…"` 这类引号包裹的密钥值（后无冒号/等号）
    .replace(/\b(?:api[_-]?key|token|authorization)\s+["'][^"']*["']/giu, '')
    .replace(/""|''/gu, '')
    .replace(/\s+/gu, ' ')
    .trim()

  // 每条规则命中即至少消耗 1 个字符，循环到不动点必然终止
  let previous = ''
  while (previous !== detail) {
    previous = detail
    for (const { pattern, to } of PROBE_DETAIL_PEELS) {
      detail = detail.replace(pattern, to).trim()
    }
  }

  if (!detail) {
    return ''
  }

  if (detail.length > 180) {
    return `${detail.slice(0, 180)}…`
  }

  return detail
}
