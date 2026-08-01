/**
 * 安全上下文与受限 Web API 能力检测框架。
 *
 * 大量 Web API（crypto.randomUUID、navigator.clipboard、crypto.subtle、
 * Service Worker 等）仅在安全上下文（HTTPS 或 localhost）可用。当应用通过
 * 明文 HTTP 部署到局域网 IP / 域名时，这些 API 会缺失或抛错。
 *
 * 本模块以「能力探测表」为中心，统一描述每个功能所需的检测条件与其 i18n 标签，
 * 供启动期提醒、各功能入口守卫复用。新增受影响功能时，只需在 CAPABILITIES 表中
 * 增加一项，并在对应入口调用 `isCapabilityBlocked('xxx')`。
 */

/** 受安全上下文限制的能力标识（与各功能入口对应） */
export type SecureCapability =
  /** 依赖 crypto.randomUUID（上传任务 ID 生成） */
  | 'fileUpload'
  /** 依赖 navigator.clipboard（复制到剪贴板） */
  | 'clipboard'

/** 当前页面 origin（用于给出"将该地址标记为可信"的指引） */
export const currentOrigin = (): string => {
  if (typeof window === 'undefined' || !window.location) return ''
  return `${window.location.protocol}//${window.location.host}`
}

/** 是否处于安全上下文（HTTPS / localhost） */
export const isSecureContext = (): boolean => {
  if (typeof window === 'undefined') {
    return true
  }
  return typeof window.isSecureContext === 'boolean' ? window.isSecureContext : false
}

/** crypto.randomUUID 是否可用 */
export const hasRandomUuid = (): boolean =>
  typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'

/** navigator.clipboard 是否可用 */
export const hasClipboard = (): boolean =>
  typeof navigator !== 'undefined' &&
  typeof navigator.clipboard !== 'undefined' &&
  typeof navigator.clipboard.writeText === 'function'

/**
 * 能力探测表：每项描述一个受安全上下文限制的能力。
 *
 * - `detect`：返回该能力当前是否受阻（true = 受阻不可用）
 * - `labelKey`：该能力受阻时展示给用户的功能名称 i18n 路径
 *
 * 新增受影响功能时在此表添加一项即可。
 */
const CAPABILITIES: Record<SecureCapability, { detect: () => boolean; labelKey: string }> = {
  fileUpload: {
    detect: () => !isSecureContext() || !hasRandomUuid(),
    labelKey: 'secureContext.capabilities.fileUpload',
  },
  clipboard: {
    detect: () => !hasClipboard(),
    labelKey: 'secureContext.capabilities.clipboard',
  },
}

/** 指定能力是否因当前环境而受阻 */
export const isCapabilityBlocked = (capability: SecureCapability): boolean =>
  CAPABILITIES[capability].detect()

/** 是否存在任意一项能力受阻（用于判断是否需要展示启动提醒） */
export const hasAnyBlockedCapability = (): boolean =>
  (Object.keys(CAPABILITIES) as SecureCapability[]).some((capability) =>
    CAPABILITIES[capability].detect(),
  )

/** 受阻能力的 i18n 标签路径集合（供启动提醒拼出"受影响功能清单"） */
export const blockedCapabilityLabelKeys = (): string[] =>
  (Object.keys(CAPABILITIES) as SecureCapability[])
    .filter((capability) => CAPABILITIES[capability].detect())
    .map((capability) => CAPABILITIES[capability].labelKey)
