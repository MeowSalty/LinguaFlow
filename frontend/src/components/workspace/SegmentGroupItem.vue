<script setup lang="ts">
import { NButton, NCheckbox, NIcon, NTooltip } from 'naive-ui'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

interface SegmentGroup {
  group_key: string
  group_title: string
  segment_count: number
  translated_count: number
  approved_count: number
}

const props = defineProps<{
  group: SegmentGroup
  selected?: boolean
}>()

const emit = defineEmits<{
  click: [groupKey: string]
  toggleSelect: [groupKey: string]
}>()

const { t } = useI18n()

const progressPercent = computed(() => {
  if (props.group.segment_count === 0) return 0
  return Math.round((props.group.translated_count / props.group.segment_count) * 100)
})

const approvedPercent = computed(() => {
  if (props.group.segment_count === 0) return 0
  return Math.round((props.group.approved_count / props.group.segment_count) * 100)
})
</script>

<template>
  <div
    class="group relative overflow-hidden rounded-lg border border-transparent bg-lf-surface/80 px-4 py-2.5 transition-all hover:border-lf-border-soft hover:bg-lf-surface-elevated hover:shadow-sm hover:shadow-lf-shadow"
  >
    <!-- 进度背景层：双重重叠进度条 -->
    <div
      class="pointer-events-none absolute inset-y-0 left-0 bg-blue-500/10 transition-all duration-500"
      :style="{ width: `${progressPercent}%` }"
    />
    <div
      class="pointer-events-none absolute inset-y-0 left-0 bg-emerald-500/10 transition-all duration-500"
      :style="{ width: `${approvedPercent}%` }"
    />
    <div class="flex min-h-14 items-center gap-3">
      <NCheckbox
        :checked="selected"
        class="shrink-0"
        @update:checked="emit('toggleSelect', group.group_key)"
        @click.stop
      />
      <div
        class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-blue-50 text-blue-600 dark:bg-blue-500/15 dark:text-blue-300"
      >
        <NIcon size="14"><IconCarbonDocument /></NIcon>
      </div>
      <div class="flex min-w-0 flex-1 items-center justify-between gap-3">
        <div class="min-w-0 flex-1">
          <!-- 主行：章节标题 + 段落数 -->
          <div class="flex min-w-0 items-center gap-2">
            <NTooltip trigger="hover" placement="top-start">
              <template #trigger>
                <span
                  class="block min-w-0 truncate text-sm font-medium text-lf-text-strong"
                  :title="group.group_title"
                >
                  {{ group.group_title }}
                </span>
              </template>
              <span class="block max-w-xs break-all">{{ group.group_title }}</span>
            </NTooltip>
            <span class="shrink-0 text-xs text-lf-text-muted">
              {{ group.segment_count }} {{ t('workspace.resource.columns.segments') }}
            </span>
          </div>
          <!-- 辅助行：group_key 路径 + 进度 -->
          <div
            class="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-lf-text-subtle"
          >
            <NTooltip trigger="hover" placement="top-start">
              <template #trigger>
                <span
                  class="inline-flex min-w-0 max-w-[24rem] items-center gap-1 truncate"
                  :title="group.group_key"
                >
                  <IconCarbonTreeView class="h-3 w-3 shrink-0" />
                  <span class="truncate">{{ group.group_key }}</span>
                </span>
              </template>
              <span class="block max-w-sm break-all">{{ group.group_key }}</span>
            </NTooltip>
            <span class="text-[10px] text-blue-500/80"> {{ progressPercent }}%</span>
            <span class="text-[10px] text-emerald-500/80"> {{ approvedPercent }}%</span>
          </div>
        </div>

        <!-- 操作按钮 -->
        <div class="flex shrink-0 items-center gap-1">
          <NButton
            size="tiny"
            quaternary
            type="primary"
            @click.stop="emit('click', group.group_key)"
          >
            <template #icon>
              <NIcon size="14"><IconCarbonView /></NIcon>
            </template>
          </NButton>
        </div>
      </div>
    </div>
  </div>
</template>
