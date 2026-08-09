<script setup lang="ts">
import { NButton, NEmpty, NSkeleton, NTag, useMessage } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { useAdminStore } from '@/stores/admin'

const admin = useAdminStore()
const message = useMessage()
const { t } = useI18n()

const formatTime = (dateStr: string): string => {
  const date = new Date(dateStr)
  const now = new Date()
  const diff = now.getTime() - date.getTime()
  const minutes = Math.floor(diff / 60000)
  const hours = Math.floor(diff / 3600000)
  const days = Math.floor(diff / 86400000)

  if (minutes < 1) return t('dashboard.activity.relativeTime.justNow')
  if (minutes < 60) return t('dashboard.activity.relativeTime.minutesAgo', { count: minutes })
  if (hours < 24) return t('dashboard.activity.relativeTime.hoursAgo', { count: hours })
  return t('dashboard.activity.relativeTime.daysAgo', { count: days })
}

const getActionType = (action: string): 'success' | 'warning' | 'error' | 'info' | 'default' => {
  if (action.includes('create') || action.includes('approve') || action.includes('retry')) {
    return 'success'
  }
  if (action.includes('update') || action.includes('sync') || action.includes('preview')) {
    return 'info'
  }
  if (action.includes('delete') || action.includes('reject') || action.includes('cancel')) {
    return 'error'
  }
  return 'default'
}

const getActionLabel = (action: string): string => {
  const map: Array<[string, string]> = [
    ['job.create', t('admin.auditLogs.actions.jobCreate')],
    ['job.cancel', t('admin.auditLogs.actions.jobCancel')],
    ['job.retry', t('admin.auditLogs.actions.jobRetry')],
    ['segment.translation_preview.apply', t('admin.auditLogs.actions.segmentPreviewApply')],
    ['segment.approve_all', t('admin.auditLogs.actions.segmentApproveAll')],
    ['segment.retranslate_rejected', t('admin.auditLogs.actions.segmentRetranslateRejected')],
    ['segment.batch_review', t('admin.auditLogs.actions.segmentBatchReview')],
    ['segment.approve', t('admin.auditLogs.actions.segmentApprove')],
    ['segment.reject', t('admin.auditLogs.actions.segmentReject')],
    ['glossary.sync_execute', t('admin.auditLogs.actions.glossarySync')],
    ['quick_translate', t('admin.auditLogs.actions.quickTranslate')],
    ['segment.update', t('admin.auditLogs.actions.segmentUpdate')],
  ]

  for (const [key, label] of map) {
    if (action.includes(key)) return label
  }
  return action
}

const columns = computed(() => [
  {
    title: t('admin.auditLogs.columns.time'),
    key: 'created_at',
  },
  {
    title: t('admin.auditLogs.columns.actor'),
    key: 'actor',
  },
  {
    title: t('admin.auditLogs.columns.action'),
    key: 'action',
  },
  {
    title: t('admin.auditLogs.columns.resource'),
    key: 'resource_type',
  },
])

onMounted(() => {
  admin.loadAuditLogs(true)
})

watch(
  () => admin.auditLogsError,
  (err) => {
    if (err) {
      message.error(err, { duration: 0, closable: true })
      admin.auditLogsError = null
    }
  },
)
</script>

<template>
  <div class="lf-page">
    <section class="lf-page-header">
      <div class="flex flex-col gap-6 lg:flex-row lg:items-center lg:justify-between">
        <div class="space-y-3">
          <div class="lf-eyebrow">
            {{ t('admin.eyebrow') }}
          </div>
          <div>
            <h1 class="text-3xl font-semibold tracking-tight text-lf-text-strong">
              {{ t('admin.auditLogs.title') }}
            </h1>
            <p class="mt-2 max-w-2xl text-sm leading-6 text-lf-text-muted">
              {{ t('admin.auditLogs.description') }}
            </p>
          </div>
        </div>
        <NButton secondary :loading="admin.auditLogsLoading" @click="admin.loadAuditLogs(true)">
          {{ t('admin.auditLogs.refresh') }}
        </NButton>
      </div>
    </section>

    <div class="lf-panel lf-table overflow-hidden">
      <div v-if="admin.auditLogsLoading" class="space-y-3 p-5">
        <NSkeleton v-for="i in 5" :key="i" text :repeat="1" class="h-10" />
      </div>

      <NEmpty
        v-else-if="admin.auditLogs.length === 0"
        class="py-16"
        :description="t('admin.auditLogs.empty')"
      />

      <div v-else class="lf-data-list">
        <div
          class="lf-data-list__head grid-cols-1 md:grid-cols-[160px_140px_140px_160px_minmax(0,1fr)]"
        >
          <span v-for="col in columns" :key="col.key" class="hidden md:block">
            {{ col.title }}
          </span>
          <span class="hidden md:block">{{ t('admin.auditLogs.columns.details') }}</span>
        </div>

        <div
          v-for="log in admin.auditLogs"
          :key="log.id"
          class="lf-data-list__row grid-cols-1 md:grid-cols-[160px_140px_140px_160px_minmax(0,1fr)]"
        >
          <div class="min-w-0">
            <div class="text-xs text-lf-text-subtle md:hidden">
              {{ t('admin.auditLogs.columns.time') }}
            </div>
            <span class="text-sm text-lf-text-muted">{{ formatTime(log.created_at) }}</span>
          </div>

          <div class="min-w-0">
            <div class="text-xs text-lf-text-subtle md:hidden">
              {{ t('admin.auditLogs.columns.actor') }}
            </div>
            <span class="truncate text-sm text-lf-text">{{ log.actor?.username ?? '-' }}</span>
          </div>

          <div class="flex items-center gap-1">
            <div class="text-xs text-lf-text-subtle md:hidden">
              {{ t('admin.auditLogs.columns.action') }}
            </div>
            <NTag size="small" round :bordered="false" :type="getActionType(log.action)">
              {{ getActionLabel(log.action) }}
            </NTag>
          </div>

          <div class="min-w-0">
            <div class="text-xs text-lf-text-subtle md:hidden">
              {{ t('admin.auditLogs.columns.resource') }}
            </div>
            <span class="truncate font-mono text-sm text-lf-text">
              {{ log.resource_type ?? '-' }}
            </span>
          </div>

          <div class="min-w-0">
            <div class="text-xs text-lf-text-subtle md:hidden">
              {{ t('admin.auditLogs.columns.details') }}
            </div>
            <span class="text-sm text-lf-text-muted">{{ log.message ?? '-' }}</span>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
