<script setup lang="ts">
import {
  NButton,
  NDropdown,
  NIcon,
  NPopconfirm,
  NTag,
  NText,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'

type Resource = ApiSchemas['Resource']

const props = defineProps<{
  resource: Resource
  replacing?: boolean
  incrementalUpdating?: boolean
  downloading?: boolean
  deleting?: boolean
}>()

const emit = defineEmits<{
  openSegments: [resource: Resource]
  replace: [resource: Resource]
  incrementalUpdate: [resource: Resource]
  download: [resource: Resource]
  delete: [resource: Resource]
}>()

const { t } = useI18n()

const formatDate = (value?: string): string => {
  if (!value) {
    return t('workspace.common.noDate')
  }

  return new Intl.DateTimeFormat('zh-Hans', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(value))
}

const statusTagType = (status: string): 'success' | 'error' | 'default' => {
  switch (status) {
    case 'ready':
      return 'success'
    case 'error':
      return 'error'
    default:
      return 'default'
  }
}

const getStatusLabel = (status: Resource['status']): string =>
  t(`workspace.resource.status.${status}`)

const dropdownOptions = computed(() => [
  { label: t('workspace.resource.actions.replace'), key: 'replace' },
  { label: t('workspace.resource.actions.incrementalUpdate'), key: 'incrementalUpdate' },
  { label: t('workspace.common.download'), key: 'download' },
])

const handleDropdownSelect = (key: string) => {
  switch (key) {
    case 'replace':
      emit('replace', props.resource)
      break
    case 'incrementalUpdate':
      emit('incrementalUpdate', props.resource)
      break
    case 'download':
      emit('download', props.resource)
      break
  }
}
</script>

<template>
  <div
    class="rounded-lg border border-transparent bg-lf-surface-muted px-4 py-3 transition-colors hover:border-lf-border hover:bg-lf-surface"
  >
    <div class="flex items-center gap-3">
      <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-md bg-blue-50 text-blue-500 dark:bg-blue-500/10">
        <NIcon size="18"><IconLucideFile /></NIcon>
      </div>
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-2">
          <span class="truncate text-sm font-medium text-lf-text-strong">
            {{ props.resource.name }}
          </span>
          <NTag size="small" :bordered="false">{{ props.resource.format || '-' }}</NTag>
          <NTag size="small" :type="statusTagType(props.resource.status)" :bordered="false">
            {{ getStatusLabel(props.resource.status) }}
          </NTag>
        </div>
        <div class="mt-1 flex items-center gap-3 text-xs text-lf-text-muted">
          <span v-if="props.resource.path !== props.resource.name" class="truncate">
            {{ props.resource.path }}
          </span>
          <span>{{ t('workspace.resource.columns.segments') }}: {{ props.resource.total_segments }}</span>
          <span>{{ formatDate(props.resource.updated_at) }}</span>
        </div>
        <NText
          v-if="props.resource.status === 'error' && props.resource.error_message"
          type="error"
          class="mt-1 block text-xs"
        >
          {{ props.resource.error_message }}
        </NText>
      </div>

      <!-- 操作按钮：始终可见 -->
      <div class="flex shrink-0 items-center gap-1">
        <NButton
          size="small"
          quaternary
          type="primary"
          @click="emit('openSegments', props.resource)"
        >
          {{ t('workspace.resource.actions.segments') }}
        </NButton>
        <NDropdown
          :options="dropdownOptions"
          trigger="click"
          @select="handleDropdownSelect"
        >
          <NButton size="small" quaternary>
            <template #icon>
              <NIcon><IconLucideMoreHorizontal /></NIcon>
            </template>
          </NButton>
        </NDropdown>
        <NPopconfirm
          :positive-text="t('workspace.common.confirm')"
          :negative-text="t('workspace.common.cancel')"
          @positive-click="emit('delete', props.resource)"
        >
          <template #trigger>
            <NButton size="small" quaternary type="error" :loading="props.deleting">
              <template #icon>
                <NIcon><IconLucideTrash2 /></NIcon>
              </template>
            </NButton>
          </template>
          {{ t('workspace.resource.deleteConfirm', { name: props.resource.name }) }}
        </NPopconfirm>
      </div>
    </div>
  </div>
</template>
