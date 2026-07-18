<script setup lang="ts">
import {
  NButton,
  NEmpty,
  NSkeleton,
  NTag,
  NDataTable,
  useMessage,
  type DataTableColumns,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import { useAdminStore } from '@/stores/admin'

type Activity = ApiSchemas['Activity']

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
  if (action.includes('create') || action.includes('register')) return 'success'
  if (action.includes('update') || action.includes('edit')) return 'info'
  if (action.includes('delete') || action.includes('disable')) return 'error'
  if (action.includes('login') || action.includes('logout')) return 'warning'
  return 'default'
}

const getActionLabel = (action: string): string => {
  const actionMap: Record<string, string> = {
    create: t('dashboard.activity.actions.create'),
    update: t('dashboard.activity.actions.update'),
    delete: t('dashboard.activity.actions.delete'),
    complete: t('dashboard.activity.actions.complete'),
    fail: t('dashboard.activity.actions.fail'),
    approve: t('dashboard.activity.actions.approve'),
    reject: t('dashboard.activity.actions.reject'),
  }

  for (const [key, label] of Object.entries(actionMap)) {
    if (action.includes(key)) return label
  }
  return action
}

const columns = computed<DataTableColumns<Activity>>(() => [
  {
    title: t('admin.auditLogs.columns.time'),
    key: 'created_at',
    width: 160,
    render: (row) => h('span', { class: 'text-sm text-lf-text-muted' }, formatTime(row.created_at)),
  },
  {
    title: t('admin.auditLogs.columns.actor'),
    key: 'actor',
    width: 140,
    render: (row) => h('span', { class: 'text-sm text-lf-text' }, row.actor?.username ?? '-'),
  },
  {
    title: t('admin.auditLogs.columns.action'),
    key: 'action',
    width: 140,
    render: (row) =>
      h(
        NTag,
        { size: 'small', type: getActionType(row.action), round: true, bordered: false },
        { default: () => getActionLabel(row.action) },
      ),
  },
  {
    title: t('admin.auditLogs.columns.resource'),
    key: 'resource_type',
    width: 160,
    render: (row) =>
      h('span', { class: 'font-mono text-sm text-lf-text' }, row.resource_type ?? '-'),
  },
  {
    title: t('admin.auditLogs.columns.details'),
    key: 'message',
    ellipsis: { tooltip: true },
    render: (row) => h('span', { class: 'text-sm text-lf-text-muted' }, row.message ?? '-'),
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

      <div v-else class="p-1 sm:p-2">
        <NDataTable
          :columns="columns"
          :data="admin.auditLogs"
          :bordered="false"
          :single-line="true"
          size="small"
        />
      </div>
    </div>
  </div>
</template>
