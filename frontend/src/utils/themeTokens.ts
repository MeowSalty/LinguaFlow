/**
 * 读取 tailwind.css 中定义的 --lf-* 设计 token。
 *
 * CSS 变量是全应用唯一的色值来源：亮/暗主题通过 html[data-theme] 切换变量值，
 * naive-ui 主题（App.vue）与自定义样式都从这里取值，避免出现两份手工同步的色值副本。
 */

export const LF_TOKEN_NAMES = [
  '--lf-bg',
  '--lf-bg-soft',
  '--lf-surface',
  '--lf-surface-elevated',
  '--lf-surface-muted',
  '--lf-text',
  '--lf-text-strong',
  '--lf-text-muted',
  '--lf-text-subtle',
  '--lf-border',
  '--lf-border-soft',
  '--lf-border-strong',
  '--lf-hover',
  '--lf-code-bg',
  '--lf-brand-50',
  '--lf-brand-100',
  '--lf-brand-400',
  '--lf-brand-500',
  '--lf-brand-600',
  '--lf-brand-700',
  '--lf-brand-soft',
  '--lf-info',
  '--lf-info-soft',
  '--lf-success',
  '--lf-success-soft',
  '--lf-danger',
  '--lf-danger-soft',
  '--lf-warning',
  '--lf-warning-soft',
  '--lf-shadow',
  '--lf-shadow-strong',
  '--lf-shadow-1',
  '--lf-shadow-2',
  '--lf-shadow-3',
] as const

export type LfTokenName = (typeof LF_TOKEN_NAMES)[number]
export type LfTokenState = Partial<Record<LfTokenName, string>>

export const readLfTokens = (): LfTokenState => {
  if (typeof window === 'undefined') {
    return {}
  }

  const styles = getComputedStyle(document.documentElement)
  const tokens: LfTokenState = {}
  for (const name of LF_TOKEN_NAMES) {
    const value = styles.getPropertyValue(name).trim()
    if (value) {
      tokens[name] = value
    }
  }
  return tokens
}
