import { type ApiSchemas, type DownloadFileResult } from '@/api/client'
import { t } from '@/i18n'

type TranslationJob = ApiSchemas['TranslationJob']

/**
 * 格式化日期为中文格式 (yyyy/MM/dd HH:mm)
 */
export const formatDate = (value?: string): string => {
  if (!value) {
    return t('workspace.common.noDate')
  }

  return new Intl.DateTimeFormat('zh-Hans', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(value))
}

/**
 * 根据状态字符串返回 naive-ui Tag 的类型
 */
type StatusTagType = 'default' | 'success' | 'warning' | 'error' | 'info'

export const statusTagType = (status: string): StatusTagType => {
  switch (status) {
    case 'ready':
    case 'completed':
    case 'translated':
    case 'edited':
    case 'approved':
      return 'success'
    case 'processing':
    case 'pending':
    case 'running':
      return 'info'
    case 'error':
    case 'failed':
    case 'rejected':
      return 'error'
    case 'cancelled':
      return 'warning'
    default:
      return 'default'
  }
}

/**
 * 获取段落状态的显示标签
 */
export const getSegmentStatusLabel = (status: string): string =>
  t(`workspace.segment.status.${status}`, status)

/**
 * 获取任务状态的显示标签
 */
export const getJobStatusLabel = (status: TranslationJob['status']): string =>
  t(`workspace.job.status.${status}`)

/**
 * 获取任务触发类型的显示标签
 */
export const getJobTriggerLabel = (trigger: TranslationJob['trigger_type']): string =>
  t(`workspace.job.trigger.${trigger}`)

/**
 * 计算任务进度百分比
 */
export const getJobProgress = (job: TranslationJob): number => {
  if (job.total_segments <= 0) {
    return job.status === 'completed' ? 100 : 0
  }

  return Math.round((job.completed_segments / job.total_segments) * 100)
}

/**
 * 触发浏览器下载
 */
export const triggerBrowserDownload = (file: DownloadFileResult, fallbackName: string): void => {
  const url = URL.createObjectURL(file.blob)
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = file.filename || fallbackName
  anchor.click()
  URL.revokeObjectURL(url)
}

/**
 * 格式化配置值为可读字符串
 */
export const formatConfigValue = (value: unknown): string => {
  if (value === null || value === undefined || value === '') {
    return '-'
  }

  if (Array.isArray(value)) {
    return value.length > 0 ? value.join(', ') : '-'
  }

  if (typeof value === 'object') {
    return JSON.stringify(value, null, 2)
  }

  return String(value)
}
