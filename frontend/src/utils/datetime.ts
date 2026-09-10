import { i18n } from '@/i18n'

export type DateTimeFormattable = string | number | Date

/** formatRelativeTime 的「N 天前」显示上限，超过后回退到 short 日期格式 */
const RELATIVE_TIME_MAX_DAYS = 30

/**
 * 以当前 i18n locale（i18n.global.locale.value）格式化日期时间，
 * 各调用点的差异通过 options 传入 Intl.DateTimeFormatOptions 覆盖。
 */
export const formatDateTime = (
  value: DateTimeFormattable,
  options: Intl.DateTimeFormatOptions,
): string => new Intl.DateTimeFormat(i18n.global.locale.value, options).format(new Date(value))

/**
 * 将日期格式化为相对时间：刚刚 → N 分钟前 → N 小时前 → N 天前（上限 30 天），
 * 超过 30 天回退到 short 形态的 formatDateTime；空值或非法日期返回占位文本。
 */
export const formatRelativeTime = (
  value: DateTimeFormattable | string | null | undefined,
): string => {
  if (!value) return i18n.global.t('common.noDate')

  const date = new Date(value)
  const time = date.getTime()
  if (Number.isNaN(time)) return i18n.global.t('common.noDate')

  const { t, d } = i18n.global
  const diffMs = Date.now() - time

  if (diffMs < 0) return t('dashboard.activity.relativeTime.justNow')

  const diffSeconds = Math.floor(diffMs / 1000)
  const diffMinutes = Math.floor(diffSeconds / 60)
  const diffHours = Math.floor(diffMinutes / 60)
  const diffDays = Math.floor(diffHours / 24)

  if (diffSeconds < 60) return t('dashboard.activity.relativeTime.justNow')
  if (diffMinutes < 60)
    return t('dashboard.activity.relativeTime.minutesAgo', { count: diffMinutes })
  if (diffHours < 24) return t('dashboard.activity.relativeTime.hoursAgo', { count: diffHours })
  if (diffDays < RELATIVE_TIME_MAX_DAYS)
    return t('dashboard.activity.relativeTime.daysAgo', { count: diffDays })

  return d(date, 'short')
}
