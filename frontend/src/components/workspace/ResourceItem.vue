<script setup lang="ts">
import {
  NButton,
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
  () => props.replacing || props.incrementalUpdating || props.downloading || props.deleting,
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
    case 'delete':
      confirmDelete()
      break
  }
}
</script>

<template>
  <div
    class="group rounded-lg border border-transparent bg-lf-surface/80 px-4 py-3 transition-all hover:border-lf-border-soft hover:bg-lf-surface-elevated hover:shadow-sm hover:shadow-lf-shadow"
  >
    <div class="flex min-h-19 items-start gap-3">
      <div
        class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-blue-50 text-blue-600 dark:bg-blue-500/15 dark:text-blue-300"
      >
        <NIcon size="18"><IconLucideFile /></NIcon>
      </div>
      <div class="flex min-w-0 flex-1 flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div class="min-w-0 flex-1">
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
              size="small"
              :type="statusTagType(props.resource.status)"
              :bordered="false"
            >
              {{ getStatusLabel(props.resource.status) }}
            </NTag>
          </div>
          <div
            class="mt-1.5 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-xs text-lf-text-muted"
          >
            <NTooltip
              v-if="props.resource.path !== props.resource.name"
              trigger="hover"
              placement="top-start"
            >
              <template #trigger>
                <span
                  class="inline-flex min-w-0 max-w-full items-center gap-1.5 sm:max-w-[min(36rem,50vw)]"
                  :title="props.resource.path"
                >
                  <IconLucideFolderTree class="h-3.5 w-3.5 shrink-0 text-lf-text-subtle" />
                  <span class="truncate">{{ props.resource.path }}</span>
                </span>
              </template>
              <span class="block max-w-sm break-all">{{ props.resource.path }}</span>
            </NTooltip>
            <span class="inline-flex shrink-0 items-center gap-1.5">
              <IconLucideRows3 class="h-3.5 w-3.5 text-lf-text-subtle" />
              {{ props.resource.total_segments }} {{ t('workspace.resource.columns.segments') }}
            </span>
            <span class="inline-flex shrink-0 items-center gap-1.5">
              <IconLucideClock3 class="h-3.5 w-3.5 text-lf-text-subtle" />
              {{ formatDate(props.resource.updated_at) }}
            </span>
            <span
              class="shrink-0 rounded-full bg-lf-surface-muted px-2 py-0.5 text-[11px] uppercase tracking-wide text-lf-text-subtle dark:bg-lf-surface-elevated dark:text-slate-300"
            >
              {{ props.resource.format || '-' }}
            </span>
          </div>
          <NText
            v-if="props.resource.status === 'error' && props.resource.error_message"
            type="error"
            class="mt-1 block truncate text-xs"
            :title="props.resource.error_message"
          >
            {{ props.resource.error_message }}
          </NText>
        </div>

        <!-- 操作按钮 -->
        <div
          class="flex w-full shrink-0 items-center justify-end gap-1 opacity-100 transition-opacity sm:w-auto md:opacity-80 md:group-hover:opacity-100 md:group-focus-within:opacity-100"
        >
          <NButton
            class="hidden sm:inline-flex"
            size="small"
            quaternary
            type="primary"
            @click="emit('openSegments', props.resource)"
          >
            {{ t('workspace.resource.actions.segments') }}
          </NButton>
          <NDropdown :options="dropdownOptions" trigger="click" @select="handleDropdownSelect">
            <NButton
              size="small"
              quaternary
              :loading="
                props.replacing || props.incrementalUpdating || props.downloading || props.deleting
              "
            >
              <template #icon>
                <NIcon><IconLucideMoreHorizontal /></NIcon>
              </template>
            </NButton>
          </NDropdown>
        </div>
      </div>
    </div>
  </div>
</template>
