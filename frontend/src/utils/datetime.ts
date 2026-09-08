import { i18n } from '@/i18n'

export type DateTimeFormattable = string | number | Date

/**
 * 以当前 i18n locale（i18n.global.locale.value）格式化日期时间，
 * 各调用点的差异通过 options 传入 Intl.DateTimeFormatOptions 覆盖。
 */
export const formatDateTime = (
  value: DateTimeFormattable,
  options: Intl.DateTimeFormatOptions,
): string => new Intl.DateTimeFormat(i18n.global.locale.value, options).format(new Date(value))
