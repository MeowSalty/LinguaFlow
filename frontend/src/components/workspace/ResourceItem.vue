<script setup lang="ts">
import {
  NButton,
  NCheckbox,
  NDropdown,
  NIcon,
  NTag,
  NText,
  NTooltip,
  useDialog,
  type DropdownOption,
} from 'naive-ui'
import { h } from 'vue'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'

type Resource = ApiSchemas['Resource']

const props = defineProps<{
  resource: Resource
  replacing?: boolean
  incrementalUpdating?: boolean
  downloading?: boolean
  downloadingTranslated?: boolean
  deleting?: boolean
  /** 翻译进度百分比（0-100） */
  progress?: number
  /** 是否处于选中状态 */
  selected?: boolean
}>()

const emit = defineEmits<{
  openSegments: [resource: Resource]
  replace: [resource: Resource]
  incrementalUpdate: [resource: Resource]
  download: [resource: Resource]
  downloadTranslated: [resource: Resource]
  delete: [resource: Resource]
  /** 切换选中状态 */
  toggleSelect: [resource: Resource]
}>()

const { t } = useI18n()
const dialog = useDialog()

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

const isBusy = computed(
  () =>
    props.replacing ||
    props.incrementalUpdating ||
    props.downloading ||
    props.downloadingTranslated ||
    props.deleting,
)

const dropdownOptions = computed<DropdownOption[]>(() => [
  {
    label: t('workspace.resource.actions.segments'),
    key: 'openSegments',
    disabled: isBusy.value,
  },
  {
    type: 'divider',
    key: 'primaryDivider',
  },
  {
    label: props.replacing
      ? t('workspace.resource.actions.replacing')
      : t('workspace.resource.actions.replace'),
    key: 'replace',
    disabled: isBusy.value,
  },
  {
    label: props.incrementalUpdating
      ? t('workspace.resource.actions.incrementalUpdating')
      : t('workspace.resource.actions.incrementalUpdate'),
    key: 'incrementalUpdate',
    disabled: isBusy.value,
  },
  {
    label: props.downloading
      ? t('workspace.resource.actions.downloading')
      : t('workspace.common.download'),
    key: 'download',
    disabled: isBusy.value,
  },
  {
    label: props.downloadingTranslated
      ? t('workspace.resource.actions.downloadingTranslated')
      : t('workspace.resource.actions.downloadTranslated'),
    key: 'download-translated',
    disabled: isBusy.value,
  },
  {
    type: 'divider',
    key: 'dangerDivider',
  },
  {
    label: () =>
      h('span', { class: 'text-red-500 dark:text-red-300' }, t('workspace.common.delete')),
    key: 'delete',
    disabled: isBusy.value,
  },
])

const confirmDelete = (): void => {
  dialog.warning({
    title: t('workspace.common.delete'),
    content: t('workspace.resource.deleteConfirm', { name: props.resource.name }),
    positiveText: t('workspace.common.confirm'),
    negativeText: t('workspace.common.cancel'),
    positiveButtonProps: {
      type: 'error',
    },
    onPositiveClick: () => emit('delete', props.resource),
  })
}

const handleDropdownSelect = (key: string) => {
  switch (key) {
    case 'openSegments':
      emit('openSegments', props.resource)
      break
    case 'replace':
      emit('replace', props.resource)
      break
    case 'incrementalUpdate':
      emit('incrementalUpdate', props.resource)
      break
    case 'download':
      emit('download', props.resource)
      break
    case 'download-translated':
      emit('downloadTranslated', props.resource)
      break
    case 'delete':
      confirmDelete()
      break
  }
}
</script>

<template>
  <div
    class="group relative overflow-hidden rounded-lg border border-transparent bg-lf-surface/80 px-4 py-2.5 transition-all hover:border-lf-border-soft hover:bg-lf-surface-elevated hover:shadow-sm hover:shadow-lf-shadow"
  >
    <!-- 进度背景层 -->
    <div
      class="pointer-events-none absolute inset-y-0 left-0 bg-emerald-500/10 transition-all duration-500"
      :style="{ width: `${props.progress ?? 0}%` }"
    />
    <div class="flex min-h-14 items-center gap-3">
      <NCheckbox
        :checked="props.selected"
        :disabled="props.resource.status !== 'ready'"
        class="shrink-0"
        @update:checked="emit('toggleSelect', props.resource)"
      />
      <div
        class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-blue-50 text-blue-600 dark:bg-blue-500/15 dark:text-blue-300"
      >
        <NIcon size="14"><IconCarbonDocument /></NIcon>
      </div>
      <div class="flex min-w-0 flex-1 items-center justify-between gap-3">
        <div class="min-w-0 flex-1">
          <!-- 主行：文件名 + 状态 + 段落数 -->
          <div class="flex min-w-0 items-center gap-2">
            <NTooltip trigger="hover" placement="top-start">
              <template #trigger>
                <span
                  class="block min-w-0 truncate text-sm font-medium text-lf-text-strong"
                  :title="props.resource.name"
                >
                  {{ props.resource.name }}
                </span>
              </template>
              <span class="block max-w-xs break-all">{{ props.resource.name }}</span>
            </NTooltip>
            <NTag
              class="shrink-0"
              size="tiny"
              :type="statusTagType(props.resource.status)"
              :bordered="false"
            >
              {{ getStatusLabel(props.resource.status) }}
            </NTag>
            <span class="shrink-0 text-xs text-lf-text-muted">
              {{ props.resource.total_segments }} {{ t('workspace.resource.columns.segments') }}
            </span>
          </div>
          <!-- 辅助行：路径 + 时间 + 格式 -->
          <div
            class="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-lf-text-subtle"
          >
            <NTooltip
              v-if="props.resource.path !== props.resource.name"
              trigger="hover"
              placement="top-start"
            >
              <template #trigger>
                <span
                  class="inline-flex min-w-0 max-w-[24rem] items-center gap-1 truncate"
                  :title="props.resource.path"
                >
                  <IconCarbonTreeView class="h-3 w-3 shrink-0" />
                  <span class="truncate">{{ props.resource.path }}</span>
                </span>
              </template>
              <span class="block max-w-sm break-all">{{ props.resource.path }}</span>
            </NTooltip>
            <span class="shrink-0">{{ formatDate(props.resource.updated_at) }}</span>
            <span
              class="shrink-0 rounded bg-lf-surface-muted px-1.5 py-px text-[10px] uppercase tracking-wider"
            >
              {{ props.resource.format || '-' }}
            </span>
            <span v-if="props.progress !== undefined" class="text-[10px] text-emerald-500/80">
              {{ props.progress }}%
            </span>
          </div>
          <!-- 错误信息 -->
          <NText
            v-if="props.resource.status === 'error' && props.resource.error_message"
            type="error"
            class="mt-0.5 block truncate text-xs"
            :title="props.resource.error_message"
          >
            {{ props.resource.error_message }}
          </NText>
        </div>

        <!-- 操作按钮：始终可见 -->
        <div class="flex shrink-0 items-center gap-1">
          <NButton
            size="tiny"
            quaternary
            type="primary"
            @click="emit('openSegments', props.resource)"
          >
            <template #icon>
              <NIcon size="14"><IconCarbonView /></NIcon>
            </template>
          </NButton>
          <NDropdown :options="dropdownOptions" trigger="click" @select="handleDropdownSelect">
            <NButton
              size="tiny"
              quaternary
              :loading="
                props.replacing ||
                props.incrementalUpdating ||
                props.downloading ||
                props.downloadingTranslated ||
                props.deleting
              "
            >
              <template #icon>
                <NIcon size="14"><IconCarbonOverflowMenuHorizontal /></NIcon>
              </template>
            </NButton>
          </NDropdown>
        </div>
      </div>
    </div>
  </div>
</template>
