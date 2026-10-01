<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { Operation } from '@/api/operations'
import { formatRelativeTime } from '@/utils/datetime'

defineProps<{
  title: string
  items: Operation[]
  loading: boolean
  error: string | null
  emptyText: string
  query: Record<string, string>
}>()
const { t } = useI18n()
const taskLink = (task: Operation) => ({
  path: '/operations',
  query: {
    task_type: task.task_type,
    task_id: task.task_id,
    ...(task.task_type === 'glossary_sync' ? { project_id: String(task.project_id) } : {}),
  },
})
const tones = {
  pending: 'default',
  running: 'info',
  paused: 'warning',
  completed: 'success',
  failed: 'error',
  cancelled: 'default',
} as const
</script>
<template>
  <section class="lf-panel h-full p-5">
    <div class="mb-4 flex items-center justify-between gap-3">
      <h2 class="text-sm font-semibold text-lf-text-strong">{{ title }}</h2>
      <RouterLink
        :to="{ path: '/operations', query }"
        class="shrink-0 text-xs text-brand-600 no-underline hover:underline"
        >{{ t('workbench.home.viewAll') }}</RouterLink
      >
    </div>
    <div v-if="loading && !items.length" class="space-y-3">
      <NSkeleton v-for="n in 3" :key="n" height="50px" />
    </div>
    <NAlert
      v-if="error"
      type="warning"
      class="mb-3"
      :title="items.length ? t('workbench.home.refreshFailed') : undefined"
      >{{ error }}</NAlert
    >
    <NEmpty v-if="!loading && !error && !items.length" :description="emptyText" class="py-8" />
    <ul v-if="items.length" class="divide-y divide-lf-border-soft">
      <li v-for="task in items" :key="`${task.task_type}:${task.task_id}`">
        <RouterLink
          :to="taskLink(task)"
          class="flex items-center justify-between gap-3 rounded-lg py-3 text-lf-text no-underline hover:bg-lf-surface focus-visible:outline-2 focus-visible:outline-brand-500"
        >
          <div class="min-w-0">
            <p class="truncate text-sm font-medium text-lf-text-strong">{{ task.project_name }}</p>
            <p class="mt-1 truncate text-xs text-lf-text-muted">
              {{ t(`workbench.home.${task.task_type}`) }} ·
              {{ t('workbench.home.task', { id: task.task_id }) }}
            </p>
            <time :datetime="task.updated_at" class="mt-1 block text-xs text-lf-text-subtle">{{
              formatRelativeTime(task.updated_at)
            }}</time>
          </div>
          <NTag :type="tones[task.status]" size="small" :bordered="false" class="shrink-0">{{
            t(`workbench.home.statuses.${task.status}`)
          }}</NTag>
        </RouterLink>
      </li>
    </ul>
  </section>
</template>
