import { t } from '@/i18n'

/** Detail-only formatting; batch inspection and other screens retain their existing formats. */
export const formatDetailDuration = (seconds: number | null): string => {
  if (seconds == null || !Number.isFinite(seconds) || seconds < 0) return '—'
  if (seconds > 0 && seconds < 1) {
    return t('workspace.job.detail.milliseconds', { count: Math.round(seconds * 1000) })
  }
  const rounded = Math.round(seconds)
  if (rounded < 60) return t('workspace.job.duration.seconds', { count: rounded })
  const minutes = Math.floor(rounded / 60)
  const remainingSeconds = rounded % 60
  if (minutes < 60) {
    return remainingSeconds
      ? t('workspace.job.detail.minutesSeconds', { minutes, seconds: remainingSeconds })
      : t('workspace.job.duration.minutes', { count: minutes })
  }
  const hours = Math.floor(minutes / 60)
  const remainingMinutes = minutes % 60
  return remainingMinutes
    ? t('workspace.job.duration.hoursMinutes', { hours, minutes: remainingMinutes })
    : t('workspace.job.duration.hours', { count: hours })
}
