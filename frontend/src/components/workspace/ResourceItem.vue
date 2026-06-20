<script setup lang="ts">
import {
  NButton,
  NCheckbox,
  NDropdown,
  NIcon,
  NTooltip,
  useDialog,
  type DropdownOption,
} from 'naive-ui'
import { computed, h } from 'vue'
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
  /** 进入资源（EPUB 虚拟目录） */
  open: [resource: Resource]
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

const formatConfig = computed(() => {
  const format = props.resource.format
  const map: Record<string, { bgClass: string; textClass: string }> = {
    epub: {
      bgClass: 'bg-indigo-50 dark:bg-indigo-500/15',
      textClass: 'text-indigo-600 dark:text-indigo-300',
    },
    json: {
      bgClass: 'bg-emerald-50 dark:bg-emerald-500/15',
      textClass: 'text-emerald-600 dark:text-emerald-300',
    },
    srt: {
      bgClass: 'bg-purple-50 dark:bg-purple-500/15',
      textClass: 'text-purple-600 dark:text-purple-300',
    },
  }
  return (
    map[format] ?? {
      bgClass: 'bg-blue-50 dark:bg-blue-500/15',
      textClass: 'text-blue-600 dark:text-blue-300',
    }
  )
})

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

/** EPUB 资源可点击进入虚拟目录 */
const isEpub = computed(() => props.resource.format === 'epub')

const handleRowClick = (): void => {
  if (isEpub.value) {
    emit('open', props.resource)
  }
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
    :class="[
      'group relative overflow-hidden rounded-lg border border-transparent bg-lf-surface/80 px-4 py-2.5 transition-all hover:border-lf-border-soft hover:bg-lf-surface-elevated hover:shadow-sm hover:shadow-lf-shadow',
    ]"
    @click="handleRowClick"
  >
    <!-- 进度背景层 -->
    <div
      class="pointer-events-none absolute inset-y-0 left-0 bg-emerald-500/10 transition-all duration-500"
      :style="{ width: `${props.progress ?? 0}%` }"
    />
    <div class="flex min-h-14 items-center gap-3">
      <NCheckbox
        :checked="props.selected"
        class="shrink-0"
        @click.stop
        @update:checked="emit('toggleSelect', props.resource)"
      />
      <div
        :class="[
          'flex h-7 w-7 shrink-0 items-center justify-center rounded-md',
          formatConfig.bgClass,
          formatConfig.textClass,
        ]"
      >
        <NIcon v-if="props.resource.format === 'epub'" size="14"><IconCarbonDocument /></NIcon>
        <NIcon v-else-if="props.resource.format === 'json'" size="14"><IconCarbonDocument /></NIcon>
        <NIcon v-else-if="props.resource.format === 'srt'" size="14"><IconCarbonDocument /></NIcon>
        <NIcon v-else size="14"><IconCarbonDocument /></NIcon>
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
        </div>

        <!-- 操作按钮：始终可见 -->
        <div class="flex shrink-0 items-center gap-1">
          <!-- 非 EPUB 资源的查看按钮 -->
          <NButton
            v-if="!isEpub"
            size="tiny"
            quaternary
            type="primary"
            @click.stop="emit('openSegments', props.resource)"
          >
            <template #icon>
              <NIcon size="14"><IconCarbonView /></NIcon>
            </template>
          </NButton>
          <!-- 操作菜单（始终显示） -->
          <NDropdown :options="dropdownOptions" trigger="click" @select="handleDropdownSelect">
            <NButton
              size="tiny"
              quaternary
              @click.stop
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
          <!-- EPUB 箭头指示器（最右侧，与文件夹一致） -->
          <NIcon
            v-if="isEpub"
            size="16"
            class="shrink-0 text-lf-text-muted opacity-60 transition-all group-hover:translate-x-0.5 group-hover:opacity-100"
          >
            <IconCarbonChevronRight />
          </NIcon>
        </div>
      </div>
    </div>
  </div>
</template>
