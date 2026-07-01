import { type ApiSchemas, type DownloadFileResult } from '@/api/client'
import type { BatchEventMetadata, SSEEvent } from '@/composables/sseShared'
import { normalizeSSELevel } from '@/composables/sseShared'
import { t } from '@/i18n'

type TranslationJob = ApiSchemas['TranslationJob']
type TranslationJobResource = ApiSchemas['TranslationJobResource']

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

// ── 阶段名称映射 ──

/** 将后端阶段标识转为用户可读的中文标签 */
export const getStageLabel = (stage: string | undefined): string => {
  if (!stage) return ''
  return t(`workspace.job.stage.${stage}`, stage)
}

// ── 进度文案 ──

/**
 * 生成进度描述文案，整合阶段、段落计数、队列信息
 */
export const getJobProgressText = (job: TranslationJob): string => {
  if (job.status === 'pending') {
    if (job.queue_position != null && job.queue_position > 1) {
      return t('workspace.job.progress.queued', { ahead: job.queue_position - 1 })
    }
    if (job.queue_position === 1) {
      return t('workspace.job.progress.startingSoon')
    }
    return t('workspace.job.progress.waiting')
  }

  if (job.status === 'running') {
    const stage = job.current_stage ? `${getStageLabel(job.current_stage)} · ` : ''
    return t('workspace.job.progress.running', {
      stage,
      completed: job.completed_segments,
      total: job.total_segments,
    })
  }

  if (job.status === 'completed') return t('workspace.job.progress.completed')
  if (job.status === 'failed') return t('workspace.job.progress.failed')
  if (job.status === 'cancelled') return t('workspace.job.progress.cancelled')
  return ''
}

// ── ETA 计算 ──

/**
 * 计算预估剩余秒数。
 * 返回 null 表示无法计算（未开始、无完成段落、已完成）。
 */
export const calculateJobETA = (job: TranslationJob): number | null => {
  if (!job.started_at || job.completed_segments < 3) return null
  if (job.status !== 'running') return null

  const elapsed = (Date.now() - new Date(job.started_at).getTime()) / 1000
  if (elapsed <= 0) return null

  const speed = job.completed_segments / elapsed
  const remaining = job.total_segments - job.completed_segments
  return remaining / speed
}

/** 将秒数格式化为人类可读的中文时长 */
export const formatETA = (seconds: number | null): string => {
  if (seconds === null || seconds <= 0) return ''

  const minutes = Math.ceil(seconds / 60)
  if (minutes < 1) return t('workspace.job.eta.lessThanOneMin')
  if (minutes < 60) return t('workspace.job.eta.minutes', { count: minutes })

  const hours = Math.floor(minutes / 60)
  const remainMinutes = minutes % 60
  if (remainMinutes === 0) return t('workspace.job.eta.hours', { count: hours })
  return t('workspace.job.eta.hoursMinutes', { hours, minutes: remainMinutes })
}

// ── 速度计算 ──

/**
 * 计算当前翻译速度（段落/分钟）。
 * 返回 null 表示无法计算。
 * 建议在 completed_segments >= 3 后再展示。
 */
export const calculateJobSpeed = (job: TranslationJob): number | null => {
  if (!job.started_at || job.completed_segments < 3) return null
  if (job.status !== 'running') return null

  const elapsed = (Date.now() - new Date(job.started_at).getTime()) / 1000
  if (elapsed <= 0) return null

  return (job.completed_segments / elapsed) * 60 // 转为 段落/分钟
}

/** 将速度格式化为可读文案，如 "3.2 段落/分钟" */
export const formatJobSpeed = (speed: number | null): string => {
  if (speed === null || speed <= 0) return ''
  if (speed < 1) return t('workspace.job.speed.verySlow')
  return t('workspace.job.speed.perMinute', { count: speed.toFixed(1) })
}

// ── 资源级阶段进度 ──

/** 获取资源的阶段进度文案，如 "翻译 18/30" */
export const getResourceStageProgress = (resource: TranslationJobResource): string => {
  if (!resource.current_stage || !resource.stage_total) return ''
  const label = getStageLabel(resource.current_stage)
  return `${label} ${resource.stage_completed ?? 0}/${resource.stage_total}`
}

// ── 批次事件工具 ──

/** 格式化耗时（ms → 人类可读） */
export const formatDuration = (ms: number): string => {
  if (ms < 1000) return `${ms}ms`
  const seconds = ms / 1000
  if (seconds < 60) return `${seconds.toFixed(1)}s`
  const minutes = seconds / 60
  return `${minutes.toFixed(1)}min`
}

/** 格式化 Token 数（如 1.2k） */
export const formatTokens = (count: number): string => {
  if (count < 1000) return String(count)
  return `${(count / 1000).toFixed(1)}k`
}

/** 判断是否为批次事件 */
export const isBatchEvent = (type: string): boolean => type === 'batch'

export const batchStatusTimelineType = (
  status: BatchEventMetadata['status'] | undefined,
  level: SSEEvent['level'],
): 'success' | 'warning' | 'error' => {
  if (status === 'failed') return 'error'
  if (status === 'partial') return 'warning'
  if (status === 'success') return 'success'
  const normalized = normalizeSSELevel(level)
  if (normalized === 'error') return 'error'
  if (normalized === 'warning') return 'warning'
  return 'success'
}

// ── 事件工具 ──

/** 事件级别对应的 naive-ui 类型 */
export const eventLevelType = (level: SSEEvent['level']): 'info' | 'warning' | 'error' =>
  normalizeSSELevel(level)
